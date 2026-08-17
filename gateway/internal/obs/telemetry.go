package obs

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/cognigate/gateway/internal/store"
)

// Sink persists a usage record. In a compose deployment this is the analytics
// client; in --dev it is the in-memory store.
//
// The signature matches store.Store's own, so either implementation is a Sink
// without an adapter in between.
type Sink interface {
	RecordUsage(ctx context.Context, rec *store.UsageRecord) error
}

// Telemetry is the bounded, asynchronous path from a served request to durable
// usage accounting.
//
// GW-11.AC-3 is the reason it is bounded and asynchronous: the gateway must keep
// serving traffic when analytics is down. An unbounded queue would trade a
// visible outage for an invisible one — memory climbing until the process is
// killed — so a full buffer drops the record and says so, loudly, on a counter.
// Losing usage rows is bad; losing the request the customer is paying for is
// worse.
type Telemetry struct {
	sink    Sink
	ch      chan store.UsageRecord
	log     *slog.Logger
	metrics *Metrics

	wg   sync.WaitGroup
	stop chan struct{}
	once sync.Once

	// warned throttles the drop warning: once the buffer is full it stays full,
	// and a line per dropped record would bury the incident in its own noise.
	warnMu     sync.Mutex
	lastWarned time.Time

	// lastStallWarn throttles the delivery-stalled warning. Touched only by the
	// dispatcher goroutine, which is the only thing that delivers, so unlike the
	// drop warning above it needs no lock.
	lastStallWarn time.Time
}

const (
	// writeTimeout bounds one delivery attempt.
	writeTimeout = 10 * time.Second
	// stallWarnEvery throttles the warning that says deliveries are failing.
	stallWarnEvery = 30 * time.Second
)

// retryBase and retryMax bound the wait between attempts. Retrying is unbounded
// in time on purpose: GW-11 asks that usage buffered while analytics is down
// appear once it returns, and an attempt budget would quietly decide how long an
// outage is allowed to last.
//
// Variables rather than constants so the tests can shorten an outage they would
// otherwise have to sit through in real seconds.
var (
	retryBase = 500 * time.Millisecond
	retryMax  = 30 * time.Second
)

// permanent is implemented by a sink error that retrying cannot fix.
//
// The analytics client's status error is the one that implements it. Matching
// on the behaviour rather than the type is what lets this package stay unaware
// of which sink it was given.
type permanent interface{ Permanent() bool }

func isPermanent(err error) bool {
	var p permanent
	return errors.As(err, &p) && p.Permanent()
}

// NewTelemetry starts the dispatcher goroutine.
func NewTelemetry(sink Sink, buffer int, log *slog.Logger, m *Metrics) *Telemetry {
	if buffer < 1 {
		buffer = 1000
	}
	t := &Telemetry{
		sink:    sink,
		ch:      make(chan store.UsageRecord, buffer),
		log:     log,
		metrics: m,
		stop:    make(chan struct{}),
	}
	t.wg.Add(1)
	go t.run()
	return t
}

// Record enqueues a usage record. It never blocks: the caller is on the request
// path, and a slow analytics backend must not become the gateway's latency.
func (t *Telemetry) Record(rec store.UsageRecord) {
	select {
	case t.ch <- rec:
	default:
		if t.metrics != nil {
			t.metrics.TelemetryDropped.Inc()
		}
		t.warn()
	}
}

func (t *Telemetry) warn() {
	t.warnMu.Lock()
	defer t.warnMu.Unlock()
	if time.Since(t.lastWarned) < 10*time.Second {
		return
	}
	t.lastWarned = time.Now()
	if t.log != nil {
		t.log.Warn("telemetry buffer full; usage records are being dropped",
			slog.Int("buffer", cap(t.ch)))
	}
}

func (t *Telemetry) run() {
	defer t.wg.Done()
	for {
		select {
		case rec := <-t.ch:
			if !t.deliver(rec, true) {
				t.lost(1)
			}
		case <-t.stop:
			// Drain what is already queued before exiting, so a clean shutdown
			// does not throw away accounting for requests that were served.
			t.drain()
			return
		}
	}
}

// drain makes one delivery attempt per queued record and gives up on the rest.
//
// A shutdown has a deadline, and retrying against an analytics service that may
// itself be stopping would spend the whole of it and still lose the records.
// GW-11 documents up to telemetry.buffer records as at risk across a restart;
// the warning below is what says how many actually were.
func (t *Telemetry) drain() {
	var lost int
	for {
		select {
		case rec := <-t.ch:
			if !t.deliver(rec, false) {
				lost++
			}
		default:
			if lost > 0 {
				t.lost(lost)
				if t.log != nil {
					t.log.Warn("usage records were discarded at shutdown",
						slog.Int("count", lost))
				}
			}
			return
		}
	}
}

// lost accounts for usage records that will never be persisted.
func (t *Telemetry) lost(n int) {
	if t.metrics != nil {
		t.metrics.TelemetryDropped.Add(float64(n))
	}
}

// deliver writes one record, reporting whether it was persisted.
//
// With retry set, a transient failure is retried until it succeeds or the
// process is shutting down. That is what makes an analytics restart survivable
// (GW-11): the record at the head of the queue waits, the queue fills behind
// it, and when the service answers again the whole backlog drains in order.
// Records that arrive after the buffer is full are dropped by Record, with the
// warning that says so — buffer, then drop loudly, is the documented behaviour.
//
// A permanent failure is not retried. Replaying a record the service has
// already refused would block every record behind it forever, which would turn
// one malformed row into total metering loss.
func (t *Telemetry) deliver(rec store.UsageRecord, retry bool) bool {
	backoff := retryBase
	for attempt := 1; ; attempt++ {
		// The write gets its own timeout rather than inheriting the request's:
		// the request is long finished, and its context has been cancelled.
		ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
		// The queue holds values, so this pointer is to the goroutine's own
		// copy: a sink that retains it cannot reach another request's record.
		err := t.sink.RecordUsage(ctx, &rec)
		cancel()

		switch {
		case err == nil:
			if attempt > 1 && t.log != nil {
				t.log.Info("usage records are reaching analytics again",
					slog.Int("attempts", attempt),
					slog.Int("queued", len(t.ch)))
			}
			return true

		case !retry || isPermanent(err):
			if t.log != nil {
				t.log.Warn("usage record could not be persisted",
					slog.String("request_id", rec.RequestID),
					slog.String("error", err.Error()))
			}
			return false
		}

		t.warnStalled(rec, err, attempt)
		select {
		case <-time.After(backoff):
		case <-t.stop:
			// Shutting down mid-outage. Say so through the same path a queued
			// record takes, rather than retrying into a closing process.
			if t.log != nil {
				t.log.Warn("usage record could not be persisted",
					slog.String("request_id", rec.RequestID),
					slog.String("error", err.Error()))
			}
			return false
		}
		backoff = min(backoff*2, retryMax)
	}
}

// warnStalled reports that deliveries are failing: once when they start to,
// then at intervals for as long as they keep failing.
func (t *Telemetry) warnStalled(rec store.UsageRecord, err error, attempt int) {
	if t.log == nil {
		return
	}
	if attempt > 1 && time.Since(t.lastStallWarn) < stallWarnEvery {
		return
	}
	t.lastStallWarn = time.Now()
	t.log.Warn("usage records are not reaching analytics; buffering and retrying",
		slog.String("request_id", rec.RequestID),
		slog.Int("attempt", attempt),
		slog.Int("queued", len(t.ch)),
		slog.Int("buffer", cap(t.ch)),
		slog.String("error", err.Error()))
}

// Close stops the dispatcher after draining the queue.
func (t *Telemetry) Close() {
	t.once.Do(func() {
		close(t.stop)
		t.wg.Wait()
	})
}
