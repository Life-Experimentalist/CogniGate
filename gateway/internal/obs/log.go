package obs

import (
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
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: ParseLevel(level)})
	return slog.New(h)
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
