// Package events delivers the gateway's event registry to a tenant's webhooks.
//
// It sits behind server.Emitter so nothing on the request path ever waits for a
// webhook: Emit queues and returns, and delivery — including every retry —
// happens on this package's own goroutines. A tenant whose endpoint is down must
// slow down nothing but their own notifications.
package events

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cognigate/gateway/internal/httpx"
	"github.com/cognigate/gateway/internal/store"
)

// The registry of event types. A webhook may only subscribe to one of these, and
// the admin API rejects anything else at creation rather than accepting a
// subscription that could never fire.
const (
	QuotaThresholdCrossed = "quota.threshold_crossed"
	QuotaHardCapReached   = "quota.hard_cap_reached"
	BreakerOpened         = "breaker.opened"
	BreakerClosed         = "breaker.closed"
	CatalogModelAdded     = "catalog.model_added"
	CatalogModelRemoved   = "catalog.model_removed"
	AliasDegraded         = "alias.degraded"
	RuleDegraded          = "rule.degraded"
)

// Registry is that list, in the order the documentation presents it. It is the
// single source of truth: the admin API validates subscriptions against it and
// /admin/v1/meta publishes it, so a type added here becomes subscribable without
// a second edit somewhere that could be forgotten.
var Registry = []string{
	QuotaThresholdCrossed,
	QuotaHardCapReached,
	BreakerOpened,
	BreakerClosed,
	CatalogModelAdded,
	CatalogModelRemoved,
	AliasDegraded,
	RuleDegraded,
}

// Envelope is the delivered body. It is the same shape for every event type;
// what differs is Data, which is documented per type.
//
// It carries no prompt or completion content, and no field derived from one
// (GW-14) — a webhook is an outbound channel to a third-party endpoint, which is
// the last place request content should ever surface.
type Envelope struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Created time.Time      `json:"created"`
	Tenant  string         `json:"tenant"`
	Data    map[string]any `json:"data"`
}

// Options configures a Dispatcher. The zero value of each field falls back to
// the GW-5 default, so a caller may set only what it means to change.
type Options struct {
	// MaxAttempts is the total number of deliveries per webhook, first try
	// included. Default 5.
	MaxAttempts int
	// Timeout bounds a single attempt. Default 10s.
	Timeout time.Duration
	// BaseBackoff is the delay before the second attempt; each further attempt
	// doubles it. Default 5s.
	BaseBackoff time.Duration
	// Queue bounds the number of events awaiting delivery. Default 256.
	Queue int
	// Workers is how many deliveries proceed at once. Default 8.
	Workers int

	// Client is the HTTP client used for delivery. Default is a client with no
	// global timeout — the per-attempt deadline is a context, so a slow endpoint
	// cannot outlive Timeout regardless.
	Client *http.Client
	// Logger defaults to slog.Default().
	Logger *slog.Logger
	// Now defaults to time.Now and exists so tests can stamp deterministically.
	Now func() time.Time
}

// Dispatcher fans one event out to every webhook subscribed to its type.
type Dispatcher struct {
	store store.Store
	opts  Options

	queue chan delivery
	wg    sync.WaitGroup
	stop  chan struct{}
	once  sync.Once
}

// delivery is one event bound for one endpoint. Fanning out at enqueue time
// rather than at delivery time is deliberate: two endpoints subscribed to the
// same event retry independently, so a single dead endpoint cannot delay or
// suppress the other's notification.
type delivery struct {
	webhook store.Webhook
	body    []byte
	eventID string
	typ     string
}

// New starts the worker pool. Nothing is dialled until an event is emitted.
func New(st store.Store, opts Options) *Dispatcher {
	if opts.MaxAttempts < 1 {
		opts.MaxAttempts = 5
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	if opts.BaseBackoff <= 0 {
		opts.BaseBackoff = 5 * time.Second
	}
	if opts.Queue < 1 {
		opts.Queue = 256
	}
	if opts.Workers < 1 {
		opts.Workers = 8
	}
	if opts.Client == nil {
		opts.Client = &http.Client{}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	d := &Dispatcher{
		store: st,
		opts:  opts,
		queue: make(chan delivery, opts.Queue),
		stop:  make(chan struct{}),
	}
	d.wg.Add(opts.Workers)
	for i := 0; i < opts.Workers; i++ {
		go d.work()
	}
	return d
}

// Emit publishes one event to every enabled webhook subscribed to its type.
//
// It never blocks and never returns an error: the caller is on a request path or
// a refresh goroutine, and neither has anywhere useful to report a webhook
// failure to. What it cannot deliver, it logs.
func (d *Dispatcher) Emit(ctx context.Context, tenantID, eventType string, data map[string]any) {
	// One id for the event, shared by the stored copy, every endpoint and every
	// retry. That is what makes the at-least-once guarantee usable: a receiver
	// seeing the same id twice knows it is a redelivery and not a second
	// occurrence, and a poller can match what it reads against what it was sent.
	envelope := Envelope{
		ID:      store.NewID(store.IDEvent),
		Type:    eventType,
		Created: d.opts.Now().UTC(),
		Tenant:  tenantID,
		Data:    data,
	}

	// Recorded before anything is delivered, and before the webhook list is even
	// read. The stored history is what a tenant with no subscription — or one
	// whose endpoint was down for all five attempts — has instead of a
	// delivery, so it must not be conditional on the delivery path working.
	if err := d.store.RecordEvent(ctx, &store.Event{
		ID:       envelope.ID,
		Type:     envelope.Type,
		Created:  envelope.Created,
		TenantID: tenantID,
		Data:     data,
	}); err != nil {
		d.opts.Logger.Warn("cannot record an event",
			slog.String("event_id", envelope.ID),
			slog.String("event_type", eventType),
			slog.String("tenant", tenantID),
			slog.String("error", err.Error()))
	}

	hooks, err := d.store.ListWebhooks(ctx, tenantID)
	if err != nil {
		d.opts.Logger.Warn("cannot list webhooks for an event",
			slog.String("tenant", tenantID),
			slog.String("event_type", eventType),
			slog.String("error", err.Error()))
		return
	}

	body, err := json.Marshal(envelope)
	if err != nil {
		d.opts.Logger.Error("cannot encode an event",
			slog.String("event_id", envelope.ID),
			slog.String("event_type", eventType),
			slog.String("error", err.Error()))
		return
	}

	for _, h := range hooks {
		if h == nil || !h.Enabled || !subscribed(h.Events, eventType) {
			continue
		}
		select {
		case d.queue <- delivery{webhook: *h, body: body, eventID: envelope.ID, typ: eventType}:
		default:
			// A full queue means deliveries are not draining. Dropping is the
			// lesser harm: the alternative is blocking whatever raised the event,
			// which is a request or a catalog refresh.
			d.opts.Logger.Warn("event queue is full; dropping a webhook delivery",
				slog.String("event_id", envelope.ID),
				slog.String("event_type", eventType),
				slog.String("webhook_id", h.ID),
				slog.Int("queue", cap(d.queue)))
		}
	}
}

// Close stops accepting work and waits for in-flight deliveries. A delivery
// waiting out its backoff abandons the wait rather than holding shutdown open
// for the full retry schedule — the event is lost, which is the trade a drain
// timeout exists to make.
func (d *Dispatcher) Close() {
	d.once.Do(func() {
		close(d.stop)
		d.wg.Wait()
	})
}

func (d *Dispatcher) work() {
	defer d.wg.Done()
	for {
		select {
		case <-d.stop:
			return
		case job := <-d.queue:
			d.deliver(job)
		}
	}
}

// deliver runs one endpoint's attempt schedule.
//
// The backoff is slept in the worker rather than scheduled on a timer, which
// costs head-of-line blocking against the other workers. That is acceptable
// here because the registry is made of state transitions — a quota crossing, a
// breaker opening — which arrive at human timescales, not per request.
func (d *Dispatcher) deliver(job delivery) {
	backoff := d.opts.BaseBackoff

	for attempt := 1; attempt <= d.opts.MaxAttempts; attempt++ {
		status, err := d.attempt(job)
		if err == nil {
			d.opts.Logger.Debug("event delivered",
				slog.String("event_id", job.eventID),
				slog.String("event_type", job.typ),
				slog.String("webhook_id", job.webhook.ID),
				slog.Int("attempt", attempt),
				slog.Int("status", status))
			return
		}

		if attempt == d.opts.MaxAttempts {
			d.opts.Logger.Warn("giving up on a webhook delivery",
				slog.String("event_id", job.eventID),
				slog.String("event_type", job.typ),
				slog.String("webhook_id", job.webhook.ID),
				slog.Int("attempts", attempt),
				slog.String("error", err.Error()))
			return
		}

		select {
		case <-d.stop:
			return
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}

// attempt makes one delivery. A non-2xx response is an error so it is retried:
// the endpoint answered, but it did not accept.
func (d *Dispatcher) attempt(job delivery) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d.opts.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.webhook.URL, bytes.NewReader(job.body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "CogniGate")
	req.Header.Set(httpx.HeaderEventID, job.eventID)
	req.Header.Set(httpx.HeaderSignature, Sign(job.webhook.Secret, job.body))

	res, err := d.opts.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	// The body is read and discarded so the connection can be reused; nothing an
	// endpoint says back is acted on.
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))

	if res.StatusCode < 200 || res.StatusCode > 299 {
		return res.StatusCode, fmt.Errorf("webhook endpoint returned %d", res.StatusCode)
	}
	return res.StatusCode, nil
}

// Sign is the value of the X-CogniGate-Signature header: an HMAC-SHA256 of the
// exact bytes delivered, keyed by the webhook's secret.
//
// Receivers must verify against the raw body they received, not against a
// re-encoding of the parsed JSON — key order and whitespace are not preserved by
// a round trip through most JSON libraries, and a re-encoded body will not
// match.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func subscribed(events []string, typ string) bool {
	for _, e := range events {
		if e == typ {
			return true
		}
	}
	return false
}
