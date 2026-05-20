package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// WebhookReceiver is a real net/http listener that routes incoming POST
// bodies to per-channel queues. Callers use WaitForEvent to block until a
// matching payload arrives or the context deadline is exceeded.
type WebhookReceiver struct {
	mu       sync.Mutex
	channels map[string]*channelQueue
	listener net.Listener
	server   *http.Server
}

// channelQueue holds the payloads received on a single logical channel.
type channelQueue struct {
	payloads []map[string]any
	notify   chan struct{} // closed and recreated on each new payload
	mu       sync.Mutex
}

// newChannelQueue returns an empty channelQueue with a ready notify channel.
func newChannelQueue() *channelQueue {
	return &channelQueue{
		notify: make(chan struct{}),
	}
}

// append adds a payload and signals waiters.
func (q *channelQueue) append(payload map[string]any) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.payloads = append(q.payloads, payload)

	// Close the current notify channel to wake all waiters, then create a new one
	// so that future appends can signal again.
	old := q.notify
	q.notify = make(chan struct{})
	close(old)
}

// snapshot returns a copy of the current payload slice.
func (q *channelQueue) snapshot() []map[string]any {
	q.mu.Lock()
	defer q.mu.Unlock()

	out := make([]map[string]any, len(q.payloads))
	copy(out, q.payloads)

	return out
}

// notifyChan returns the current notification channel (closed when a new
// payload arrives).
func (q *channelQueue) notifyChan() <-chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()

	return q.notify
}

// NewWebhookReceiver creates and starts a webhook receiver on the given
// address. It routes POST /hook/:channel_id to per-channel queues.
func NewWebhookReceiver(listen string) (*WebhookReceiver, error) {
	r := &WebhookReceiver{
		channels: make(map[string]*channelQueue),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/hook/", r.handleHook)

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", listen, err)
	}

	r.listener = ln
	r.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() { _ = r.server.Serve(ln) }()

	return r, nil
}

// Addr returns the actual listening address (useful when the port is :0).
func (r *WebhookReceiver) Addr() string {
	return r.listener.Addr().String()
}

// Close shuts the HTTP server down.
func (r *WebhookReceiver) Close(ctx context.Context) error {
	return r.server.Shutdown(ctx)
}

// handleHook handles POST /hook/:channel_id.
func (r *WebhookReceiver) handleHook(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract channel ID from path: /hook/<channelID>
	channelID := strings.TrimPrefix(req.URL.Path, "/hook/")
	channelID = strings.TrimSuffix(channelID, "/")

	if channelID == "" {
		http.Error(w, "channel_id required in path", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusInternalServerError)
		return
	}

	var payload map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			// Accept non-JSON bodies as an empty map so WaitForEvent can still match.
			payload = map[string]any{"_rawBody": string(body)}
		}
	}

	if payload == nil {
		payload = make(map[string]any)
	}

	r.getOrCreate(channelID).append(payload)
	w.WriteHeader(http.StatusOK)
}

// getOrCreate returns the channelQueue for channelID, creating it if needed.
func (r *WebhookReceiver) getOrCreate(channelID string) *channelQueue {
	r.mu.Lock()
	defer r.mu.Unlock()

	q, ok := r.channels[channelID]
	if !ok {
		q = newChannelQueue()
		r.channels[channelID] = q
	}

	return q
}

// WaitForEvent blocks until a payload in channelID satisfies predicate, or
// the context is canceled. It scans already-received payloads before waiting.
func (r *WebhookReceiver) WaitForEvent(
	ctx context.Context,
	channelID string,
	predicate func(map[string]any) bool,
) error {
	q := r.getOrCreate(channelID)

	for {
		// Check already received payloads.
		for _, p := range q.snapshot() {
			if predicate(p) {
				return nil
			}
		}

		// If context is already done, return its error.
		if err := ctx.Err(); err != nil {
			return err
		}

		// Wait for either a new payload or context cancellation.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-q.notifyChan():
			// new payload arrived — loop and check predicates again
		}
	}
}
