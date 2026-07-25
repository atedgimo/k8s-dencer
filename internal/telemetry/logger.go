// Package telemetry provides logging and observability wiring shared by all
// k8s-dencer components.
package telemetry

import (
	"log/slog"
	"os"
	"strings"
)

// NewLogger returns a structured JSON logger at the given level. Level strings
// are matched case-insensitively; anything unrecognised falls back to info so a
// typo in the chart's log level can never silence a component.
func NewLogger(component, level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler).With("component", component)
}
