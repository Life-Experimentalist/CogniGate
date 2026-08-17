package obs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cognigate/gateway/internal/store"

	dto "github.com/prometheus/client_model/go"
)

// fastRetries shortens the backoff for the duration of one test, so an outage
// that would take seconds of real waiting takes milliseconds.
func fastRetries(t *testing.T) {
	t.Helper()
	base, max := retryBase, retryMax
	retryBase, retryMax = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { retryBase, retryMax = base, max })
}

// fakeSink is an analytics service a test can take down and bring back up.
type fakeSink struct {
	mu       sync.Mutex
	failures int   // deliveries to refuse before answering
	err      error // what to refuse them with
	got      []store.UsageRecord
	attempts int
	answered chan struct{}
}

func newSink(failures int, err error) *fakeSink {
	return &fakeSink{failures: failures, err: err, answered: make(chan struct{}, 64)}
}

func (f *fakeSink) RecordUsage(_ context.Context, rec *store.UsageRecord) error {
	f.mu.Lock()
	f.attempts++
	if f.failures > 0 {
		f.failures--
		err := f.err
		f.mu.Unlock()
		return err
	}
	f.got = append(f.got, *rec)
	f.mu.Unlock()
	f.answered <- struct{}{}
	return nil
}

func (f *fakeSink) stored() []store.UsageRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.UsageRecord(nil), f.got...)
}

func (f *fakeSink) tries() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

// waitForDeliveries blocks until n records have been stored, or fails the test.
func (f *fakeSink) waitForDeliveries(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-f.answered:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d records reached the sink", len(f.stored()), n)
		}
	}
}

// permanentErr is what the analytics client returns for a 4xx.
type permanentErr struct{ msg string }

func (e permanentErr) Error() string   { return e.msg }
func (e permanentErr) Permanent() bool { return true }

// transientErr is what a stopped analytics service looks like.
var transientErr = errors.New("connection refused")

// logTo returns a logger writing into a buffer the test can read back.
func logTo(buf *syncBuffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type syncBuffer struct {
	mu sync.Mutex
	sb strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.String()
}

// waitForLog waits for a line the dispatcher writes after the delivery that
// released the test. Asserting on the buffer directly would be a race: the sink
// answers from inside RecordUsage, so the goroutine that logs the outcome has
// not necessarily reached the log call yet.
func waitForLog(t *testing.T, buf *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), want) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Errorf("%q was never logged:\n%s", want, buf.String())
}

func dropped(t *testing.T, m *Metrics) float64 {
	t.Helper()
	var out dto.Metric
	if err := m.TelemetryDropped.Write(&out); err != nil {
		t.Fatalf("reading the drop counter: %v", err)
	}
	return out.GetCounter().GetValue()
}

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRecordsReachTheSink(t *testing.T) {
	sink := newSink(0, nil)
	tel := NewTelemetry(sink, 10, discard(), NewMetrics())
	defer tel.Close()

	tel.Record(store.UsageRecord{RequestID: "r1"})
	tel.Record(store.UsageRecord{RequestID: "r2"})
	sink.waitForDeliveries(t, 2)

	got := sink.stored()
	if len(got) != 2 || got[0].RequestID != "r1" || got[1].RequestID != "r2" {
		t.Fatalf("sink holds %+v, want r1 then r2 in order", got)
	}
}

// GW-11.AC-3: with analytics stopped the data plane keeps serving, and the usage
// buffered during the outage appears once analytics is back.
func TestABufferedRecordIsReplayedWhenAnalyticsReturns(t *testing.T) {
	fastRetries(t)

	sink := newSink(4, transientErr)
	metrics := NewMetrics()
	var buf syncBuffer
	tel := NewTelemetry(sink, 10, logTo(&buf), metrics)
	defer tel.Close()

	tel.Record(store.UsageRecord{RequestID: "r1", TotalTokens: 7})
	sink.waitForDeliveries(t, 1)

	got := sink.stored()
	if len(got) != 1 || got[0].RequestID != "r1" {
		t.Fatalf("sink holds %+v, want the record that was buffered", got)
	}
	if sink.tries() != 5 {
		t.Fatalf("delivered in %d attempts, want 5 — four refusals then the write", sink.tries())
	}
	// Nothing was lost: a transient failure is an outage, not a decision to
	// discard accounting for a request the customer was charged for.
	if n := dropped(t, metrics); n != 0 {
		t.Fatalf("drop counter = %v, want 0", n)
	}

	waitForLog(t, &buf, "usage records are not reaching analytics")
	waitForLog(t, &buf, "usage records are reaching analytics again")
}

func TestTheBacklogDrainsInOrderAfterAnOutage(t *testing.T) {
	fastRetries(t)

	sink := newSink(3, transientErr)
	tel := NewTelemetry(sink, 10, discard(), NewMetrics())
	defer tel.Close()

	// The head of the queue is stuck retrying; these pile up behind it.
	for _, id := range []string{"r1", "r2", "r3"} {
		tel.Record(store.UsageRecord{RequestID: id})
	}
	sink.waitForDeliveries(t, 3)

	got := sink.stored()
	if len(got) != 3 {
		t.Fatalf("sink holds %d records, want 3", len(got))
	}
	for i, want := range []string{"r1", "r2", "r3"} {
		if got[i].RequestID != want {
			t.Fatalf("record %d is %q, want %q — the backlog reordered", i, got[i].RequestID, want)
		}
	}
}

func TestARefusedRecordIsDroppedRatherThanReplayedForever(t *testing.T) {
	fastRetries(t)

	// A 4xx says the record itself is wrong. Replaying it would block every
	// record behind it, turning one malformed row into total metering loss.
	sink := newSink(1, permanentErr{"analytics POST /api/v1/usage: 400 Bad Request"})
	metrics := NewMetrics()
	var buf syncBuffer
	tel := NewTelemetry(sink, 10, logTo(&buf), metrics)
	defer tel.Close()

	tel.Record(store.UsageRecord{RequestID: "bad"})
	tel.Record(store.UsageRecord{RequestID: "good"})
	sink.waitForDeliveries(t, 1)

	got := sink.stored()
	if len(got) != 1 || got[0].RequestID != "good" {
		t.Fatalf("sink holds %+v, want the record behind the refused one", got)
	}
	if sink.tries() != 2 {
		t.Fatalf("%d attempts, want 2 — the refusal must not be retried", sink.tries())
	}
	if n := dropped(t, metrics); n != 1 {
		t.Fatalf("drop counter = %v, want 1", n)
	}
	if log := buf.String(); !strings.Contains(log, "usage record could not be persisted") {
		t.Errorf("the drop was not logged:\n%s", log)
	}
}

func TestAFullBufferDropsLoudlyRatherThanBlocking(t *testing.T) {
	// Record runs on the request path. An unbounded queue would trade a visible
	// outage for memory climbing until the process is killed.
	blocked := make(chan struct{})
	sink := &blockingSink{release: blocked}
	metrics := NewMetrics()
	var buf syncBuffer
	tel := NewTelemetry(sink, 1, logTo(&buf), metrics)

	for i := 0; i < 50; i++ {
		tel.Record(store.UsageRecord{RequestID: "r"})
	}
	if n := dropped(t, metrics); n == 0 {
		t.Fatal("a full buffer dropped nothing")
	}
	if log := buf.String(); !strings.Contains(log, "telemetry buffer full") {
		t.Errorf("the drops were not logged:\n%s", log)
	}

	close(blocked)
	tel.Close()
}

func TestCloseDrainsWhatIsQueued(t *testing.T) {
	release := make(chan struct{})
	sink := &blockingSink{release: release, record: true}
	tel := NewTelemetry(sink, 10, discard(), NewMetrics())

	for _, id := range []string{"r1", "r2", "r3"} {
		tel.Record(store.UsageRecord{RequestID: id})
	}
	close(release)
	tel.Close()

	// A clean shutdown must not throw away accounting for requests that were
	// actually served.
	if got := sink.stored(); len(got) != 3 {
		t.Fatalf("%d of 3 records survived the shutdown", len(got))
	}
}

func TestCloseDuringAnOutageReturnsPromptly(t *testing.T) {
	fastRetries(t)

	// Retrying is unbounded in time, so shutdown must be able to interrupt it —
	// otherwise a stopped analytics service would hang every gateway restart.
	sink := newSink(1<<30, transientErr)
	metrics := NewMetrics()
	var buf syncBuffer
	tel := NewTelemetry(sink, 10, logTo(&buf), metrics)

	tel.Record(store.UsageRecord{RequestID: "r1"})
	tel.Record(store.UsageRecord{RequestID: "r2"})
	// Give the dispatcher time to pick up r1 and start retrying.
	time.Sleep(20 * time.Millisecond)

	done := make(chan struct{})
	go func() { tel.Close(); close(done) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return during an analytics outage")
	}

	// Both records are gone, and the counter says so rather than the loss being
	// silent.
	if n := dropped(t, metrics); n != 2 {
		t.Fatalf("drop counter = %v, want 2", n)
	}
	// Which of the two paths the second record takes — abandoned mid-backoff, or
	// abandoned by the drain — is up to a select the test cannot steer. Both say
	// the same thing on the way out, and that is what an operator reads.
	if log := buf.String(); !strings.Contains(log, "usage record could not be persisted") {
		t.Errorf("the shutdown loss was not logged:\n%s", log)
	}
}

func TestDrainReportsWhatShutdownLost(t *testing.T) {
	// GW-11 documents up to telemetry.buffer records as at risk across a restart.
	// This is the line that says how many actually were — driven directly, because
	// reaching it through Close means racing the dispatcher's own select.
	sink := newSink(1<<30, transientErr)
	metrics := NewMetrics()
	var buf syncBuffer
	tel := &Telemetry{
		sink:    sink,
		ch:      make(chan store.UsageRecord, 4),
		log:     logTo(&buf),
		metrics: metrics,
		stop:    make(chan struct{}),
	}
	tel.ch <- store.UsageRecord{RequestID: "r1"}
	tel.ch <- store.UsageRecord{RequestID: "r2"}

	tel.drain()

	// One attempt each and no retry: a shutdown has a deadline, and retrying
	// against an analytics service that may itself be stopping would spend the
	// whole of it and still lose the records.
	if sink.tries() != 2 {
		t.Fatalf("%d attempts, want 2 — drain must not retry", sink.tries())
	}
	if n := dropped(t, metrics); n != 2 {
		t.Fatalf("drop counter = %v, want 2", n)
	}
	log := buf.String()
	if !strings.Contains(log, "usage records were discarded at shutdown") {
		t.Errorf("the shutdown loss was not logged:\n%s", log)
	}
	if !strings.Contains(log, "count=2") {
		t.Errorf("the log does not say how many were lost:\n%s", log)
	}
}

func TestDrainSaysNothingWhenNothingWasLost(t *testing.T) {
	sink := newSink(0, nil)
	var buf syncBuffer
	tel := &Telemetry{
		sink:    sink,
		ch:      make(chan store.UsageRecord, 4),
		log:     logTo(&buf),
		metrics: NewMetrics(),
		stop:    make(chan struct{}),
	}
	tel.ch <- store.UsageRecord{RequestID: "r1"}

	tel.drain()

	if got := sink.stored(); len(got) != 1 {
		t.Fatalf("drain persisted %d records, want 1", len(got))
	}
	if log := buf.String(); strings.Contains(log, "discarded at shutdown") {
		t.Errorf("a clean shutdown warned about losses it did not have:\n%s", log)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	tel := NewTelemetry(newSink(0, nil), 10, discard(), NewMetrics())
	tel.Close()
	tel.Close()
}

func TestTelemetryToleratesNoLoggerAndNoMetrics(t *testing.T) {
	fastRetries(t)

	sink := newSink(2, transientErr)
	tel := NewTelemetry(sink, 10, nil, nil)
	defer tel.Close()

	tel.Record(store.UsageRecord{RequestID: "r1"})
	sink.waitForDeliveries(t, 1)
}

func TestABufferOfZeroGetsADefault(t *testing.T) {
	tel := NewTelemetry(newSink(0, nil), 0, discard(), NewMetrics())
	defer tel.Close()

	if cap(tel.ch) < 1 {
		t.Fatalf("buffer capacity is %d", cap(tel.ch))
	}
}

func TestIsPermanentMatchesOnBehaviourNotType(t *testing.T) {
	// The obs package must not have to know which sink it was given, so the
	// classification is an interface match through whatever wrapped the error.
	if !isPermanent(permanentErr{"refused"}) {
		t.Error("a refusal was not classified as permanent")
	}
	if !isPermanent(wrap(permanentErr{"refused"})) {
		t.Error("a wrapped refusal was not classified as permanent")
	}
	if isPermanent(transientErr) {
		t.Error("an outage was classified as permanent")
	}
	if isPermanent(notPermanentErr{}) {
		t.Error("an error that says it is retryable was classified as permanent")
	}
}

type notPermanentErr struct{}

func (notPermanentErr) Error() string   { return "503" }
func (notPermanentErr) Permanent() bool { return false }

func wrap(err error) error { return errors.Join(errors.New("context"), err) }

// blockingSink holds every delivery until it is released.
type blockingSink struct {
	release chan struct{}
	record  bool

	mu  sync.Mutex
	got []store.UsageRecord
}

func (b *blockingSink) RecordUsage(_ context.Context, rec *store.UsageRecord) error {
	<-b.release
	if b.record {
		b.mu.Lock()
		b.got = append(b.got, *rec)
		b.mu.Unlock()
	}
	return nil
}

func (b *blockingSink) stored() []store.UsageRecord {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]store.UsageRecord(nil), b.got...)
}
