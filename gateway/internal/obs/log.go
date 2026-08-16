package obs

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// NewLogger builds the process logger.
//
// Output is JSON on purpose. The gateway's logs are read by a collector far more
// often than by a person, and GW-8 requires every line to be attributable to a
// request id — which only survives reliably as a structured field.
func NewLogger(level string) *slog.Logger {
	return slog.New(newHandler(os.Stdout, level))
}

// newHandler is NewLogger without its destination, so the field names it
// produces can be asserted on without capturing the process's stdout.
func newHandler(w io.Writer, level string) slog.Handler {
	return slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: ParseLevel(level),
		// GW-8 names the timestamp field `ts`; slog's own key is `time`. The
		// field list is a published contract that collectors are configured
		// against, so the specification wins over the standard library's
		// default spelling.
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				a.Key = "ts"
			}
			return a
		},
	})
}

// ParseLevel maps the configured log.level onto slog's levels. An unrecognised
// value becomes info rather than an error: a typo in a log setting should not
// stop the gateway from starting.
func ParseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
