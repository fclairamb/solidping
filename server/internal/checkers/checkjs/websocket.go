package checkjs

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/dop251/goja"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// WebSocket frame type names, as a script sees them.
const (
	wsTypeText   = "text"
	wsTypeBinary = "binary"
)

// registerWebSocket exposes `websocket.connect(url, options)`.
//
// The global collides with the `solidping.websocket()` sub-check wrapper only
// in vocabulary: the wrapper runs a WHOLE websocket check and hands back its
// verdict, this one drives a live connection frame by frame. `browser` versus
// `solidping.browser` already set that precedent.
func (r *jsRuntime) registerWebSocket() {
	obj := r.vm.NewObject()

	_ = obj.Set("connect", func(call goja.FunctionCall) goja.Value {
		return r.openWebSocket(call.Argument(0).String(), optionMap(call.Argument(1)))
	})

	_ = r.vm.Set("websocket", obj)
}

// wsHandle is one live WebSocket connection a script is driving.
type wsHandle struct {
	runtime *jsRuntime
	conn    *websocket.Conn
	closed  bool
}

// openWebSocket is websocket.connect: gate, budget, handshake.
func (r *jsRuntime) openWebSocket(url string, opts map[string]any) goja.Value {
	// Same SERVER-level gate, same message, same throw as tcp.connect.
	if TypeEnabled != nil && !TypeEnabled(checkerdef.CheckTypeWebSocket) {
		panic(r.vm.NewGoError(typeDisabledError(checkerdef.CheckTypeWebSocket)))
	}

	handle := &wsHandle{runtime: r}

	if refusal := r.spendConnectionBudget(); refusal != nil {
		return handle.toValue(refusal)
	}

	return handle.toValue(handle.dial(url, opts))
}

// dial performs the handshake and returns the handle's fields.
func (h *wsHandle) dial(url string, opts map[string]any) map[string]any {
	ctx, cancel, err := h.runtime.callContext(opts)
	if err != nil {
		return socketFailure(err)
	}

	defer cancel()

	dialOpts, tunneled := h.dialOptions(opts)

	start := time.Now()

	conn, resp, err := websocket.Dial(ctx, url, dialOpts)

	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}

	if err != nil {
		return h.handshakeFailure(ctx, err, resp, tunneled)
	}

	h.conn = conn
	h.runtime.sockets = append(h.runtime.sockets, h.dispose)

	fields := map[string]any{
		jsKeyOK:              true,
		jsKeyConnectDuration: time.Since(start).Milliseconds(),
	}

	if resp != nil {
		fields[jsKeyStatusCode] = resp.StatusCode
	}

	if tunneled {
		fields[jsKeyTunneled] = true
	}

	return fields
}

// dialOptions mirrors WebSocketChecker.dial: headers verbatim, a private
// transport ONLY when verification is off or a tunnel dialer is present, and
// coder/websocket's default client otherwise.
func (h *wsHandle) dialOptions(opts map[string]any) (*websocket.DialOptions, bool) {
	dialOpts := &websocket.DialOptions{}

	if headers, ok := opts["headers"].(map[string]any); ok && len(headers) > 0 {
		header := http.Header{}

		for name, value := range headers {
			if str, ok := value.(string); ok {
				header.Set(name, str)
			}
		}

		dialOpts.HTTPHeader = header
	}

	tlsVerify := true
	if verify, ok := opts["tlsVerify"].(bool); ok {
		tlsVerify = verify
	}

	transport := &http.Transport{}
	needClient := false

	if !tlsVerify {
		transport.TLSClientConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			// Opt-in, and exactly the websocket check's `tls_skip_verify`.
			InsecureSkipVerify: true,
		}
		needClient = true
	}

	// Tunneled: the handshake's underlying dial goes through the bastion.
	// http.Transport hands it the raw host:port, so the far side resolves a
	// private name this worker's resolver never could.
	tunneled := false

	if dialer := checkerdef.TunnelDialerFrom(h.runtime.execCtx); dialer != nil {
		transport.DialContext = dialer.DialContext
		needClient = true
		tunneled = true
	}

	if needClient {
		dialOpts.HTTPClient = &http.Client{Transport: transport}
	}

	return dialOpts, tunneled
}

// handshakeFailure keeps the rejecting HTTP status when the server answered at
// all — a `401` on the handshake is a completely different incident from a
// connection that never reached a server.
func (h *wsHandle) handshakeFailure(
	ctx context.Context, err error, resp *http.Response, tunneled bool,
) map[string]any {
	fields := socketFailuref("connection failed: %v", err)

	if resp != nil {
		fields[jsKeyStatusCode] = resp.StatusCode
	}

	if class := checkerdef.ClassifyDialError(err, ctx.Err() != nil); class != "" {
		fields[jsKeyClass] = class
	}

	if tunneled {
		fields[jsKeyTunneled] = true
	}

	return fields
}

// toValue builds the JS object: the handshake result's fields plus the methods.
func (h *wsHandle) toValue(fields map[string]any) goja.Value {
	obj := h.runtime.vm.NewObject()

	for key, value := range fields {
		_ = obj.Set(key, value)
	}

	_ = obj.Set("send", func(call goja.FunctionCall) goja.Value {
		return h.runtime.vm.ToValue(
			h.send(call.Argument(0).String(), optionMap(call.Argument(1))))
	})

	_ = obj.Set("receive", func(call goja.FunctionCall) goja.Value {
		return h.runtime.vm.ToValue(h.receive(optionMap(call.Argument(0))))
	})

	// Uncounted, like page.close() and the socket handles' close().
	_ = obj.Set("close", func(call goja.FunctionCall) goja.Value {
		h.close(optionMap(call.Argument(0)))

		return h.runtime.vm.ToValue(map[string]any{jsKeyOK: true})
	})

	return obj
}

// send writes one frame.
func (h *wsHandle) send(data string, opts map[string]any) map[string]any {
	if refusal := h.runtime.spendSocketAction(); refusal != nil {
		return refusal
	}

	if h.conn == nil || h.closed {
		return socketFailure(errSocketNotConnected)
	}

	messageType := websocket.MessageText
	if frameType, ok := opts[jsKeyType].(string); ok && frameType == wsTypeBinary {
		messageType = websocket.MessageBinary
	}

	encoding, _ := opts[optKeyEncoding].(string)

	payload, err := checkerdef.DecodePayload(data, encoding)
	if err != nil {
		return socketFailure(err)
	}

	if len(payload) > maxSocketWriteBytes {
		return socketFailuref(
			"frame of %d bytes exceeds the %d-byte per-call send cap", len(payload), maxSocketWriteBytes)
	}

	ctx, cancel, err := h.runtime.callContext(opts)
	if err != nil {
		return socketFailure(err)
	}

	defer cancel()

	start := time.Now()

	if err := h.conn.Write(ctx, messageType, payload); err != nil {
		h.runtime.panicOnExecutionDeadline()

		fields := socketFailure(err)
		fields[jsKeyBytes] = 0
		fields[jsKeyDuration] = time.Since(start).Milliseconds()

		return fields
	}

	return map[string]any{
		jsKeyOK:       true,
		jsKeyBytes:    len(payload),
		jsKeyDuration: time.Since(start).Milliseconds(),
	}
}

// receive reads one frame. coder/websocket answers pings inside Read, so a
// script never sees a control frame.
func (h *wsHandle) receive(opts map[string]any) map[string]any {
	if refusal := h.runtime.spendSocketAction(); refusal != nil {
		return refusal
	}

	if h.conn == nil || h.closed {
		return socketFailure(errSocketNotConnected)
	}

	encoding, _ := opts[optKeyEncoding].(string)
	if encoding != "" && encoding != checkerdef.PayloadEncodingText && encoding != encodingHex {
		return socketFailuref("unknown encoding %q, must be one of text, hex", encoding)
	}

	limit := maxSocketReadBytes
	if raw, ok := numericOption(opts[optKeyMaxBytes]); ok {
		limit = clampReadBytes(int(raw))
	}

	remaining := h.runtime.remainingPayload()
	if remaining <= 0 {
		return socketFailure(errPayloadPoolExhausted)
	}

	if int64(limit) > remaining {
		limit = int(remaining)
	}

	// Applied PER CALL: a frame over the limit fails and the library closes the
	// connection, which the handle reflects on the next call.
	h.conn.SetReadLimit(int64(limit))

	ctx, cancel, err := h.runtime.callContext(opts)
	if err != nil {
		return socketFailure(err)
	}

	defer cancel()

	start := time.Now()

	messageType, payload, err := h.conn.Read(ctx)

	h.runtime.spendPayload(len(payload))

	if err != nil {
		return h.receiveFailure(err, time.Since(start))
	}

	frameType := wsTypeText
	if messageType == websocket.MessageBinary {
		frameType = wsTypeBinary
	}

	return map[string]any{
		jsKeyOK:       true,
		jsKeyType:     frameType,
		jsKeyData:     encodeReadData(payload, encoding),
		jsKeyBytes:    len(payload),
		jsKeyDuration: time.Since(start).Milliseconds(),
		jsKeyTimedOut: false,
	}
}

// receiveFailure splits a failed Read the way the socket handles split theirs:
// the EXECUTION deadline panics (status `timeout`), the per-call timeout is a
// value, everything else is the target's verdict.
func (h *wsHandle) receiveFailure(err error, elapsed time.Duration) map[string]any {
	h.runtime.panicOnExecutionDeadline()

	fields := map[string]any{
		jsKeyOK:                   false,
		jsKeyBytes:                0,
		jsKeyDuration:             elapsed.Milliseconds(),
		jsKeyTimedOut:             false,
		checkerdef.OutputKeyError: err.Error(),
	}

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		fields[jsKeyTimedOut] = true
		fields[checkerdef.OutputKeyError] = "no frame before the call's timeout"
	case websocket.CloseStatus(err) != -1:
		fields[jsKeyEOF] = true
		fields[checkerdef.OutputKeyError] = fmt.Sprintf(
			"connection closed by peer (%s)", websocket.CloseStatus(err).String())
	case errors.Is(err, io.EOF):
		fields[jsKeyEOF] = true
	}

	return fields
}

// close is the script's own close({ code, reason }): a clean close frame,
// idempotent.
func (h *wsHandle) close(opts map[string]any) {
	if h.closed || h.conn == nil {
		h.closed = true

		return
	}

	h.closed = true

	code := websocket.StatusNormalClosure
	if raw, ok := numericOption(opts["code"]); ok {
		code = websocket.StatusCode(int(raw))
	}

	reason, _ := opts["reason"].(string)

	_ = h.conn.Close(code, reason)
}

// dispose is the Execute-defer path: never block on a close handshake a
// timed-out script has no budget left for.
func (h *wsHandle) dispose() {
	if h.closed || h.conn == nil {
		h.closed = true

		return
	}

	h.closed = true

	_ = h.conn.CloseNow()
}
