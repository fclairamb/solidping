package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// The out-of-band upload half of the agent protocol (spec 2026-08-21-05).
//
// The WebSocket is JSON, so binary captures cannot ride it. The agent therefore
// keeps the bytes in a local, bounded, TTL'd cache and puts only a marker on the
// result frame; when that result opens or reopens an incident the server sends
// back an `upload-request` naming the capture and the topic, and the agent POSTs
// the bytes to the one endpoint built for them.
//
// The whole path is best-effort in both directions. A capture that cannot be
// cached, a request for a capture that has already been evicted, and a POST that
// fails are all the same outcome: no screenshot on that incident. None of them
// is retried, and none of them can affect the check, the result, or the incident.

const (
	// attachmentsPath is the agent attachment upload endpoint. It is signed
	// with the SAME challenge scheme as the WS reconnect (method|path|ts|nonce),
	// so an agent needs no second credential.
	attachmentsPath = "/api/v1/agent/attachments"
	// uploadTimeout bounds one upload attempt. There is exactly one attempt.
	uploadTimeout = 30 * time.Second
	// mimePNG is the only capture kind today.
	mimePNG = "image/png"
)

// ErrUploadRejected is returned when the server refuses an attachment upload.
var ErrUploadRejected = errors.New("attachment upload rejected")

// stashCapture moves a result's screenshot bytes out of the diagnostics block
// and into the agent's local cache, replacing them with the marker the wire
// actually carries.
//
// It returns a COPY: the caller's checkerdef.Result is the checker's own
// output, and mutating it here would make "what the agent kept" depend on who
// happens to hold a reference. The PNG field is cleared on the copy even though
// it is `json:"-"` and could never serialize anyway — the invariant "the frame
// holds no bytes" should be structural, not a property of one struct tag.
//
// A capture the cache refuses (too large for the whole budget) advertises
// NOTHING: an unfulfillable marker would make the server ask for an upload that
// can never arrive.
func (b *WSBackend) stashCapture(diagnostics *checkerdef.Diagnostics) *checkerdef.Diagnostics {
	if diagnostics == nil || diagnostics.Screenshot == nil || len(diagnostics.Screenshot.PNG) == 0 {
		return diagnostics
	}

	out := *diagnostics
	shot := *diagnostics.Screenshot

	captureID := b.captures.Put(shot.PNG)
	shot.PNG = nil

	if captureID == "" {
		out.Screenshot = nil

		return &out
	}

	shot.Available = true
	shot.CaptureID = captureID
	out.Screenshot = &shot

	return &out
}

// handleUploadRequest serves one server->agent upload request. Runs off the read
// pump's goroutine so a slow upload cannot stall frame dispatch.
func (b *WSBackend) handleUploadRequest(ctx context.Context, frame *agents.ServerFrame) {
	png, ok := b.captures.Take(frame.CaptureID)
	if !ok {
		// Evicted, expired, already served, or simply never held by this
		// process (the agent restarted between the result and the request).
		// The capture is lost and that is the end of it.
		b.logger.DebugContext(ctx, "upload requested for an unknown capture",
			"capture_id", frame.CaptureID, "topic", frame.Topic)

		return
	}

	uploadCtx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()

	if err := b.uploadAttachment(uploadCtx, frame.Topic, png); err != nil {
		// ONE attempt. The bytes are already out of the cache, so there is
		// nothing to retry with even if we wanted to — see capturecache.Take.
		b.logger.WarnContext(ctx, "capture upload failed",
			"topic", frame.Topic, "bytes", len(png), "error", err)

		return
	}

	b.logger.DebugContext(ctx, "capture uploaded", "topic", frame.Topic, "bytes", len(png))
}

// uploadAttachment POSTs a capture to the agent attachment endpoint with the
// agent's normal signed headers.
func (b *WSBackend) uploadAttachment(ctx context.Context, topic string, body []byte) error {
	b.mu.Lock()
	identity := *b.identity
	b.mu.Unlock()

	if identity.AgentUID == "" {
		return ErrNotEnrolled
	}

	timestamp := time.Now().UTC().Format(time.RFC3339)

	nonce, err := randomID()
	if err != nil {
		return err
	}

	signature, err := identity.Sign(http.MethodPost, attachmentsPath, timestamp, nonce)
	if err != nil {
		return fmt.Errorf("sign attachment upload: %w", err)
	}

	endpoint := httpBaseURL(b.serverURL) + attachmentsPath + "?topic=" + url.QueryEscape(topic)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build attachment upload: %w", err)
	}

	req.Header.Set("Content-Type", mimePNG)
	req.Header.Set("X-Sp-Agent-Uid", identity.AgentUID)
	req.Header.Set("X-Sp-Timestamp", timestamp)
	req.Header.Set("X-Sp-Nonce", nonce)
	req.Header.Set("X-Sp-Signature", signature)

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("post attachment: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	// Drain a little of the body so the connection can be reused, and so a
	// refusal can be logged with the server's reason rather than a bare code.
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("%w: status %d: %s", ErrUploadRejected, resp.StatusCode, strings.TrimSpace(string(detail)))
	}

	return nil
}

// httpBaseURL turns the agent's configured server URL into an http(s) origin.
// The same value is used to dial the WebSocket, where `ws://`/`wss://` are also
// accepted, so the two schemes are normalized here rather than being a
// configuration trap.
func httpBaseURL(serverURL string) string {
	switch {
	case strings.HasPrefix(serverURL, "wss://"):
		return "https://" + strings.TrimPrefix(serverURL, "wss://")
	case strings.HasPrefix(serverURL, "ws://"):
		return "http://" + strings.TrimPrefix(serverURL, "ws://")
	default:
		return serverURL
	}
}
