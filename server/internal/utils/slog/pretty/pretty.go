// Package pretty provides slog handlers that prints the log.
package pretty

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
)

const (
	reset = "\033[0m"

	black        = 30
	red          = 31
	green        = 32
	yellow       = 33
	blue         = 34
	magenta      = 35
	cyan         = 36
	lightGray    = 37
	darkGray     = 90
	lightRed     = 91
	lightGreen   = 92
	lightYellow  = 93
	lightBlue    = 94
	lightMagenta = 95
	lightCyan    = 96
	white        = 97
)

// Handler is a slog handler that pretty prints the log.
// From: https://dusted.codes/creating-a-pretty-console-logger-using-gos-slog-package
type Handler struct {
	h         slog.Handler
	b         *bytes.Buffer
	w         io.Writer
	m         *sync.Mutex
	withColor bool
}

// NewPretty creates a new pretty handler.
func NewPretty(writer io.Writer, opts *slog.HandlerOptions) *Handler {
	if opts == nil {
		opts = &slog.HandlerOptions{}
	}
	b := &bytes.Buffer{}
	withColor := terminalSupportsColor()
	return &Handler{
		b: b,
		w: writer,
		h: slog.NewJSONHandler(b, &slog.HandlerOptions{
			Level:       opts.Level,
			AddSource:   opts.AddSource,
			ReplaceAttr: suppressDefaults(opts.ReplaceAttr),
		}),
		m:         &sync.Mutex{},
		withColor: withColor,
	}
}

// Enabled reports whether the handler handles records at the given level.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.h.Enabled(ctx, level)
}

// WithAttrs returns a new Handler whose attributes consists of.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{
		h:         h.h.WithAttrs(attrs),
		w:         h.w,
		b:         h.b,
		m:         h.m,
		withColor: h.withColor,
	}
}

// WithGroup returns a new Handler with the given group appended to.
func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{
		h:         h.h.WithGroup(name),
		w:         h.w,
		b:         h.b,
		m:         h.m,
		withColor: h.withColor,
	}
}

// Handle handles the Record.
func (h *Handler) Handle(ctx context.Context, record slog.Record) error { //nolint:gocritic
	// Format level with brackets
	level := fmt.Sprintf("[%-5s]", record.Level.String())

	var levelColor int
	switch record.Level {
	case slog.LevelDebug:
		levelColor = lightGray
	case slog.LevelInfo:
		levelColor = green
	case slog.LevelWarn:
		levelColor = yellow
	case slog.LevelError:
		levelColor = red
	}
	level = h.colorize(levelColor, level)

	attrs, err := h.computeAttrs(ctx, record)
	if err != nil {
		return err
	}

	// Format attributes as key=value pairs
	var attrStrSb strings.Builder
	for k, v := range attrs {
		if str, ok := v.(string); ok {
			fmt.Fprintf(&attrStrSb, " %s=%s", h.colorize(cyan, k), h.colorize(magenta, str))
		} else {
			fmt.Fprintf(&attrStrSb, " %s=%v", h.colorize(cyan, k), v)
		}
	}
	attrStr := attrStrSb.String()

	// Print without the timestamp and with proper spacing
	if _, err := fmt.Fprintf(h.w, "%s %-50s%s\n",
		level,
		record.Message,
		attrStr,
	); err != nil {
		return fmt.Errorf("error when writing to writer: %w", err)
	}

	return nil
}

func (h *Handler) computeAttrs(
	ctx context.Context,
	record slog.Record, //nolint:gocritic
) (map[string]any, error) {
	h.m.Lock()
	defer func() {
		h.b.Reset()
		h.m.Unlock()
	}()
	if err := h.h.Handle(ctx, record); err != nil {
		return nil, fmt.Errorf("error when calling inner handler's Handle: %w", err)
	}

	var attrs map[string]any
	err := json.Unmarshal(h.b.Bytes(), &attrs)
	if err != nil {
		return nil, fmt.Errorf("error when unmarshaling inner handler's Handle result: %w", err)
	}
	return attrs, nil
}

func (h *Handler) colorize(colorCode int, v string) string {
	if !h.withColor {
		return v
	}
	return fmt.Sprintf("\033[%dm%v%s", colorCode, v, reset)
}

func suppressDefaults(
	next func([]string, slog.Attr) slog.Attr,
) func([]string, slog.Attr) slog.Attr {
	return func(groups []string, attr slog.Attr) slog.Attr {
		if attr.Key == slog.TimeKey ||
			attr.Key == slog.LevelKey ||
			attr.Key == slog.MessageKey {
			return slog.Attr{}
		}
		if next == nil {
			return attr
		}
		return next(groups, attr)
	}
}
