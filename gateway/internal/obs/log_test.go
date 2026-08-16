package obs

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

// GW-8 lists the fields a request log line must carry, and names the timestamp
// `ts`. slog's own key is `time`, so the rename is a deliberate override of the
// standard library's default rather than an accident — and it is invisible from
// everywhere except a collector configured against the published field list.

func TestLoggerNamesTheTimestampField(t *testing.T) {
	var buf bytes.Buffer
	slog.New(newHandler(&buf, "info")).Info("request", slog.String("request_id", "req_1"))

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("the log line is not JSON: %v\n%s", err, buf.String())
	}
	if _, ok := record["ts"]; !ok {
		t.Errorf("no ts field; GW-8 names the timestamp ts, and the line carries %v", keys(record))
	}
	if _, ok := record["time"]; ok {
		t.Error("the line carries slog's default `time` key as well as `ts`, so a collector would see two timestamps")
	}
	if record["msg"] != "request" || record["request_id"] != "req_1" {
		t.Errorf("the rename disturbed other fields: %v", record)
	}
}

func TestLoggerRenamesOnlyTheTopLevelTimestamp(t *testing.T) {
	// The rename is scoped to the record's own timestamp. An attribute a caller
	// happens to name `time` inside a group is its data, not slog's key, and
	// must survive untouched.
	var buf bytes.Buffer
	slog.New(newHandler(&buf, "info")).Info("request",
		slog.Group("upstream", slog.String("time", "2026-01-01T00:00:00Z")))

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("the log line is not JSON: %v\n%s", err, buf.String())
	}
	group, ok := record["upstream"].(map[string]any)
	if !ok {
		t.Fatalf("no upstream group in %v", record)
	}
	if group["time"] != "2026-01-01T00:00:00Z" {
		t.Errorf("a caller's own `time` attribute was rewritten: %v", group)
	}
}

func TestLoggerHonoursTheConfiguredLevel(t *testing.T) {
	var buf bytes.Buffer
	slog.New(newHandler(&buf, "warn")).Info("request")
	if buf.Len() != 0 {
		t.Errorf("an info line was written at log.level=warn: %s", buf.String())
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		" WARN ":  slog.LevelWarn,
		// A typo in a log setting must not stop the gateway from starting, so
		// anything unrecognised — the empty string included — is info.
		"":        slog.LevelInfo,
		"verbose": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) is %v, want %v", in, got, want)
		}
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
