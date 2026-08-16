package conformance

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// Reading the gateway's structured request log.
//
// GW-8's first two acceptance criteria are about a log line: that one completion
// produces exactly one, carrying a fixed set of fields, and that no line carries
// request content or a whole credential. Neither is observable through the HTTP
// surface every other test in this suite uses, so this is the one place the
// suite reaches outside it.
//
// It is opt-in through CONF_LOG_PATH, and the two tests skip without it. A
// gateway that logs to stdout inside a container is conformant and unreadable
// from here; reporting that as a failure would make the suite wrong about a
// deployment rather than about an implementation. What the report then says is
// "not run", which is the honest answer.

// requireLogAccess skips unless the deployment told the suite where its log is.
func requireLogAccess(t *testing.T) {
	t.Helper()
	if suite.cfg.LogPath == "" {
		t.Skip("CONF_LOG_PATH is unset, so the gateway's request log cannot be read from here")
	}
}

// logCursor is a byte offset into the log file. Marking one before the request
// and reading from it afterwards is what makes "exactly one line" a claim about
// this test rather than about everything the process has ever served — the suite
// runs many tests against one gateway, and several may be in flight at once.
type logCursor int64

func markLog(t *testing.T) logCursor {
	t.Helper()
	info, err := os.Stat(suite.cfg.LogPath)
	if err != nil {
		t.Fatalf("CONF_LOG_PATH=%s cannot be read: %v", suite.cfg.LogPath, err)
	}
	return logCursor(info.Size())
}

func bytesSince(t *testing.T, from logCursor) []byte {
	t.Helper()

	f, err := os.Open(suite.cfg.LogPath)
	if err != nil {
		t.Fatalf("opening %s: %v", suite.cfg.LogPath, err)
	}
	defer f.Close()

	if _, err := f.Seek(int64(from), 0); err != nil {
		t.Fatalf("seeking %s to %d: %v", suite.cfg.LogPath, from, err)
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("reading %s: %v", suite.cfg.LogPath, err)
	}
	return raw
}

// rawSince returns the log bytes written after the cursor, undecoded.
//
// The leak assertions read this rather than the decoded records: a prompt that
// escaped into a panic trace, or into a line the gateway never meant to be
// structured, is exactly the leak GW-14 is about, and a reader that skipped
// unparseable lines would be blind to it.
func rawSince(t *testing.T, from logCursor) string {
	t.Helper()
	return string(bytesSince(t, from))
}

// linesSince returns the log records written after the cursor, decoded.
//
// A line that is not JSON is skipped rather than fatal. A startup banner is not
// a structured record and is not what GW-8 constrains; failing on one would make
// the suite reject a gateway for printing its version.
func linesSince(t *testing.T, from logCursor) []map[string]any {
	t.Helper()

	var out []map[string]any
	for _, line := range strings.Split(string(bytesSince(t, from)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		out = append(out, record)
	}
	return out
}

// awaitLogLines waits for the log to catch up before reading it.
//
// The line is written after the response is sent — that is the whole point of a
// duration field — so a test that read immediately would sometimes race the
// writer and see nothing. It keeps reading past the first match for the same
// reason awaitDeliveries does: "exactly one" is not proved by finding one.
func awaitLogLines(t *testing.T, from logCursor, match func(map[string]any) bool) []map[string]any {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		var got []map[string]any
		for _, record := range linesSince(t, from) {
			if match(record) {
				got = append(got, record)
			}
		}
		if len(got) > 0 {
			time.Sleep(time.Second)
			var settled []map[string]any
			for _, record := range linesSince(t, from) {
				if match(record) {
					settled = append(settled, record)
				}
			}
			return settled
		}
		if time.Now().After(deadline) {
			t.Fatalf("no matching line reached %s within 10s", suite.cfg.LogPath)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// requestLines narrows to the gateway's per-request records for one request id.
func requestLines(records []map[string]any, requestID string) []map[string]any {
	var out []map[string]any
	for _, r := range records {
		if r["msg"] == "request" && r["request_id"] == requestID {
			out = append(out, r)
		}
	}
	return out
}
