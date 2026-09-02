package obs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/cognigate/gateway/internal/store"
)

// Sink persists a usage record. In a compose deployment this is the analytics
// client; in --dev it is the in-memory store.
type Sink interface {
	RecordUsage(ctx context.Context, rec store.UsageRecord) error
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
			t.deliver(rec)
		case <-t.stop:
			// Drain what is already queued before exiting, so a clean shutdown
			// does not throw away accounting for requests that were served.
			for {
				select {
				case rec := <-t.ch:
					t.deliver(rec)
				default:
					return
				}
			}
		}
	}
}

func (t *Telemetry) deliver(rec store.UsageRecord) {
	// The write gets its own timeout rather than inheriting the request's: the
	// request is long finished, and its context has been cancelled.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := t.sink.RecordUsage(ctx, rec); err != nil && t.log != nil {
		t.log.Warn("usage record could not be persisted",
			slog.String("request_id", rec.RequestID),
			slog.String("error", err.Error()))
	}
}

// Close stops the dispatcher after draining the queue.
func (t *Telemetry) Close() {
	t.once.Do(func() {
		close(t.stop)
		t.wg.Wait()
	})
}
