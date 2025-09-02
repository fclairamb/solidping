// Package slogutil provides slog handler utilities.
package slogutil

import (
	"context"
	"log/slog"
)

// FanoutHandler forwards log records to multiple slog handlers.
type FanoutHandler struct {
	handlers []slog.Handler
}

// NewFanoutHandler creates a handler that fans out to all given handlers.
func NewFanoutHandler(handlers ...slog.Handler) *FanoutHandler {
	return &FanoutHandler{handlers: handlers}
}

// Enabled returns true if any underlying handler is enabled.
func (h *FanoutHandler) Enabled(
	ctx context.Context, level slog.Level,
) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}

	return false
}

// Handle forwards the record to all underlying handlers.
//
//nolint:gocritic // slog.Handler interface requires Record by value
func (h *FanoutHandler) Handle(
	ctx context.Context, record slog.Record,
) error {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, record.Level) {
			if err := handler.Handle(ctx, record); err != nil {
				return err
			}
		}
	}

	return nil
}

// WithAttrs returns a new FanoutHandler with attrs added.
func (h *FanoutHandler) WithAttrs(
	attrs []slog.Attr,
) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithAttrs(attrs)
	}

	return &FanoutHandler{handlers: handlers}
}

// WithGroup returns a new FanoutHandler with the group added.
func (h *FanoutHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithGroup(name)
	}

	return &FanoutHandler{handlers: handlers}
}
