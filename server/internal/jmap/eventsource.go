package jmap

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// EventSource backoff bounds (RFC 8620 §7.3 advises retry; we use exponential
// backoff capped at 5 minutes).
const (
	eventSourceBackoffStart = time.Second
	eventSourceBackoffMax   = 5 * time.Minute
)

// EventSourceHandler is invoked for each parsed SSE event. Returning an error
// causes the listener to disconnect and reconnect after backoff.
type EventSourceHandler func(EventSourceEvent) error

// ListenEventSourceWithReconnect opens an SSE connection to the discovered
// eventSourceUrl and dispatches events to handler until ctx is canceled. If
// the connection drops, it reconnects with exponential backoff (1s → 5min cap)
// reset on each successful connect. Filtering of `ping` vs real `state` events
// is the handler's responsibility.
//
// types is the comma-separated list of event types the JMAP server will emit
// (typically "state"). It is added as the `types` query parameter per RFC 8620
// §7.3. Empty string omits the parameter.
func (c *Client) ListenEventSourceWithReconnect(
	ctx context.Context, types string, handler EventSourceHandler,
) error {
	c.mu.RLock()
	url := c.eventSourceURL
	c.mu.RUnlock()

	if url == "" {
		return ErrNoSession
	}

	if types != "" {
		separator := "?"
		if strings.Contains(url, "?") {
			separator = "&"
		}

		url += separator + "types=" + types
	}

	backoff := eventSourceBackoffStart

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		err := c.streamOnce(ctx, url, handler)

		switch {
		case err == nil, errors.Is(err, io.EOF):
			// Successful round-trip: reset backoff. Most servers eventually
			// close the long-lived stream; we just reconnect.
			backoff = eventSourceBackoffStart
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return err
		default:
			slog.WarnContext(ctx, "JMAP EventSource connection lost",
				"error", err, "backoff_seconds", backoff.Seconds())
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > eventSourceBackoffMax {
			backoff = eventSourceBackoffMax
		}
	}
}

// streamOnce opens one SSE connection and dispatches events until the stream
// closes or the handler returns an error.
func (c *Client) streamOnce(ctx context.Context, url string, handler EventSourceHandler) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build SSE request: %w", err)
	}

	c.applyAuth(req)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := c.streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("open SSE: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("%w: SSE %d", ErrUnexpectedStatus, resp.StatusCode)
	}

	return parseSSE(resp.Body, handler)
}

// parseSSE reads server-sent events from r and invokes handler per event.
// Implementation follows the SSE spec as used by JMAP §7.3:
//   - lines starting `event:` set the next event type (default "message")
//   - lines starting `data:` accumulate into the data buffer
//   - blank line terminates an event and flushes it
//   - lines starting with `:` are comments and ignored
func parseSSE(r io.Reader, handler EventSourceHandler) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var (
		eventType string
		data      strings.Builder
	)

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			if eventType == "" && data.Len() == 0 {
				continue
			}

			if err := handler(EventSourceEvent{
				Type: defaultEventType(eventType),
				Data: data.String(),
			}); err != nil {
				return err
			}

			eventType = ""

			data.Reset()

			continue
		}

		if strings.HasPrefix(line, ":") {
			continue
		}

		field, value := splitField(line)

		switch field {
		case "event":
			eventType = value
		case "data":
			if data.Len() > 0 {
				data.WriteByte('\n')
			}

			data.WriteString(value)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan SSE: %w", err)
	}

	return io.EOF
}

func defaultEventType(t string) string {
	if t == "" {
		return "message"
	}

	return t
}

// splitField splits an SSE field line at the first colon. Per spec, a line
// without a colon is treated as a field name with empty value.
func splitField(line string) (string, string) {
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return line, ""
	}

	field := line[:idx]
	value := strings.TrimPrefix(line[idx+1:], " ")

	return field, value
}
