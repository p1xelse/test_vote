// Package observability собирает логгер, метрики и debug-эндпоинты сервиса.
package observability

import (
	"log/slog"
	"os"
)

// NewLogger возвращает логгер: человекочитаемый локально, JSON во всём остальном.
func NewLogger(env string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}

	var handler slog.Handler
	if env == "local" {
		opts.Level = slog.LevelDebug
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	return slog.New(handler)
}
