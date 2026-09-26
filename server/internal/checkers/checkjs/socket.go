package checkjs

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"time"

	"github.com/dop251/goja"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Budgets and caps for the `tcp` / `udp` / `websocket` globals (spec
// 2026-09-15-06 §4).
const (
	// maxSocketConnections caps how many connections ONE execution may open
	// across all three globals combined, counted at connect/open time whether
	// or not earlier ones were closed — a closed connection is one this script
	// already spent, exactly like the browser's one page.
	maxSocketConnections = 5

	// maxSocketActions is the read/write budget, counted SEPARATELY from
	// maxSubChecks for the same reason maxBrowserActions is: a conversation is
	// many cheap round-trips on one held resource, and folding it into the
	// 20-call `http.*` budget would let one Redis handshake exhaust the budget
	// the script needs for the API call it was authenticating FOR. close() is
	// uncounted — releasing a resource must never be the call that hits a
	// limit.
	maxSocketActions = 200

	// maxSocketReadBytes is the default `maxBytes` of one read/receive. The
	// ceiling is checkerdef.MaxPayloadBytes, and a UDP datagram cannot exceed
	// this anyway.
	maxSocketReadBytes = 64 * 1024

	// maxSocketWriteBytes caps one write/send. It is the script-size cap:
	// nothing a script legitimately sends is bigger than the script.
	maxSocketWriteBytes = 64 * 1024
)

// Socket result field names. `ok`, `error` and `duration` are shared with the
// rest of the runtime (jsKeyOK, checkerdef.OutputKeyError, jsKeyDuration).
const (
	jsKeyData            = "data"
	jsKeyBytes           = "bytes"
	jsKeyTimedOut        = "timedOut"
	jsKeyEOF             = "eof"
	jsKeyClass           = "class"
	jsKeyRemoteAddr      = "remoteAddr"
	jsKeyIPVersion       = "ipVersion"
	jsKeyTunneled        = "tunneled"
	jsKeyConnectDuration = "connectDuration"
	jsKeyType            = "type"
	jsKeyTLS             = "tls"

	optKeyEncoding = "encoding"
	optKeyMaxBytes = "maxBytes"
	optKeyBytes    = "bytes"
	optKeyUntil    = "until"
	optKeyPattern  = "pattern"

	// encodingHex is the one non-default OUTPUT encoding a read can ask for.
	// goja strings are UTF-16 and cannot carry raw bytes, so binary traffic
	// crosses the boundary as hex — the same vocabulary the tcp check's
	// `send_encoding` already uses.
	encodingHex = checkerdef.PayloadEncodingHex
)

// socketKind decides which method names a handle exposes and whether read
// criteria are accepted at all.
type socketKind string

const (
	socketKindTCP socketKind = "tcp"
	socketKindUDP socketKind = "udp"
)

// Errors the socket globals produce. Only errTypeDisabled ever THROWS; the rest
// are values a script inspects, per §2's returned-vs-thrown contract.
var (
	errTypeDisabled        = errors.New("is disabled on this server")
	errSocketNotConnected  = errors.New("not connected")
	errUDPCannotBeTunneled = errors.New(
		"UDP cannot be tunneled: an SSH direct-tcpip forward carries TCP only")
	errIPVersionWithTunnel = errors.New(
		"a tunneled connection is resolved and dialed by the SSH bastion, so its address family " +
			"cannot be pinned here — remove ipVersion, or remove the tunnel")
	errReadCriteriaConflict = errors.New(
		`give at most one of "bytes", "until" or "pattern"`)
	errUDPReadCriteria = errors.New(
		`udp receive reads exactly one datagram: "bytes", "until" and "pattern" are not accepted`)
	errPayloadPoolExhausted = errors.New("the execution's shared " +
		strconv.Itoa(checkerdef.MaxPayloadBytes) + "-byte payload budget is exhausted")
	errNoAddresses = errors.New("no IP addresses found for host")
)

// typeDisabledError is the SERVER-level activation gate's message, worded
// exactly like the sub-check gate's so an operator sees one sentence whichever
// door a script tried.
func typeDisabledError(checkType checkerdef.CheckType) error {
	return fmt.Errorf("check type %q %w", string(checkType), errTypeDisabled)
}

// registerTCP exposes `tcp.connect(address, options)`.
func (r *jsRuntime) registerTCP() {
	obj := r.vm.NewObject()

	_ = obj.Set("connect", func(call goja.FunctionCall) goja.Value {
		return r.openSocket(socketKindTCP, call.Argument(0).String(), optionMap(call.Argument(1)))
	})

	_ = r.vm.Set("tcp", obj)
}

// registerUDP exposes `udp.open(address, options)`.
func (r *jsRuntime) registerUDP() {
	obj := r.vm.NewObject()

	_ = obj.Set("open", func(call goja.FunctionCall) goja.Value {
		return r.openSocket(socketKindUDP, call.Argument(0).String(), optionMap(call.Argument(1)))
	})

	_ = r.vm.Set("udp", obj)
}

// optionMap reads a binding's trailing option argument, tolerating it being
// absent, undefined, or something that is not an object at all.
func optionMap(value goja.Value) map[string]any {
	if value == nil || goja.IsUndefined(value) || goja.IsNull(value) {
		return nil
	}

	opts, _ := value.Export().(map[string]any)

	return opts
}

// socketHandle is one live TCP or UDP connection a script is driving.
//
// goja is single-threaded and every binding blocks, so nothing here needs a
// mutex: closeSockets() runs from Execute's defer on the same goroutine, after
// RunString has returned.
type socketHandle struct {
	runtime *jsRuntime
	kind    socketKind
	conn    net.Conn
	closed  bool
}

// openSocket is the shared body of tcp.connect and udp.open.
func (r *jsRuntime) openSocket(kind socketKind, address string, opts map[string]any) goja.Value {
	checkType := checkerdef.CheckTypeTCP
	if kind == socketKindUDP {
		checkType = checkerdef.CheckTypeUDP
	}

	// The SERVER-level activation gate, the same one sub-checks and
	// browser.open() consult, with the same message. An operator who turned a
	// transport off does not get it back through a script — so this THROWS
	// (reported as `error`) rather than handing back a verdict.
	if TypeEnabled != nil && !TypeEnabled(checkType) {
		panic(r.vm.NewGoError(typeDisabledError(checkType)))
	}

	handle := &socketHandle{runtime: r, kind: kind}

	// Counted BEFORE anything is dialed, exactly like the sub-check and browser
	// budgets: a refused connect still spends its unit, so a script cannot spin
	// the refusal path for free.
	if refusal := r.spendConnectionBudget(); refusal != nil {
		return handle.toValue(refusal)
	}

	return handle.toValue(handle.dial(address, opts))
}

// spendConnectionBudget charges one connection against the per-execution
// connection cap AND one unit of the shared 20-call budget `http.*` and
// `solidping.*` draw from. Returns the refusal fields, or nil to proceed.
func (r *jsRuntime) spendConnectionBudget() map[string]any {
	if r.socketCount.Add(1) > int32(maxSocketConnections) {
		return socketFailuref("connection limit of %d exceeded", maxSocketConnections)
	}

	if r.subCheckCount.Add(1) > int32(maxSubChecks) {
		return socketFailuref("sub-check limit of %d exceeded", maxSubChecks)
	}

	return nil
}

// spendSocketAction charges one read/write against maxSocketActions. Returns
// the refusal fields, or nil to proceed.
func (r *jsRuntime) spendSocketAction() map[string]any {
	if r.socketActions.Add(1) > int32(maxSocketActions) {
		return socketFailuref("socket action limit of %d exceeded", maxSocketActions)
	}

	return nil
}

// socketFailure is the `{ ok: false, error }` every returned failure starts as.
func socketFailure(err error) map[string]any {
	return map[string]any{jsKeyOK: false, checkerdef.OutputKeyError: err.Error()}
}

func socketFailuref(format string, args ...any) map[string]any {
	return map[string]any{
		jsKeyOK:                   false,
		checkerdef.OutputKeyError: fmt.Sprintf(format, args...),
	}
}

// connectOptions is the parsed form of tcp.connect / udp.open's option map.
type connectOptions struct {
	tls           bool
	tlsVerify     bool
	tlsServerName string
	ipVersion     checkerdef.IPVersion
	ipVersionSet  bool
}

// parseConnectOptions reads the option map. `timeout` is deliberately NOT here:
// it goes through callContext, the one place the clamp-never-widen rule lives.
func (r *jsRuntime) parseConnectOptions(opts map[string]any) (connectOptions, error) {
	parsed := connectOptions{
		tlsVerify: true,
		ipVersion: checkerdef.IPVersionFrom(r.execCtx),
	}

	if opts == nil {
		return parsed, nil
	}

	if useTLS, ok := opts["tls"].(bool); ok {
		parsed.tls = useTLS
	}

	if verify, ok := opts["tlsVerify"].(bool); ok {
		parsed.tlsVerify = verify
	}

	if name, ok := opts["tlsServerName"].(string); ok {
		parsed.tlsServerName = name
	}

	if raw, present := opts["ipVersion"]; present && raw != nil {
		str, ok := raw.(string)
		if !ok {
			return parsed, fmt.Errorf(
				"%w: must be a string (auto, ipv4 or ipv6)", checkerdef.ErrInvalidIPVersion)
		}

		version, err := checkerdef.ParseIPVersion(str)
		if err != nil {
			return parsed, err
		}

		parsed.ipVersion, parsed.ipVersionSet = version, true
	}

	return parsed, nil
}

// dial performs the connect and returns the handle's fields.
func (h *socketHandle) dial(address string, opts map[string]any) map[string]any {
	options, err := h.runtime.parseConnectOptions(opts)
	if err != nil {
		return socketFailure(err)
	}

	ctx, cancel, err := h.runtime.callContext(opts)
	if err != nil {
		return socketFailure(err)
	}

	defer cancel()

	if dialer := checkerdef.TunnelDialerFrom(h.runtime.execCtx); dialer != nil {
		return h.dialTunneled(ctx, dialer, address, options)
	}

	return h.dialDirect(ctx, address, options)
}

// dialDirect resolves the name locally, picks an address family and dials it —
// the same three checkerdef pieces the tcp and udp checkers compose.
func (h *socketHandle) dialDirect(
	ctx context.Context, address string, options connectOptions,
) map[string]any {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return socketFailuref("invalid address %q: expected host:port", address)
	}

	start := time.Now()

	addrs, err := checkerdef.LookupIPAddr(ctx, host)
	if err != nil {
		return socketFailuref("failed to resolve hostname: %v", err)
	}

	if len(addrs) == 0 {
		return socketFailure(errNoAddresses)
	}

	targetIP, err := checkerdef.SelectIPAddr(host, addrs, options.ipVersion)
	if err != nil {
		return socketFailure(err)
	}

	// The pinned IP goes through the egress guard (spec 2026-09-25-19): under
	// an enforcing policy a non-public address is refused before connect.
	conn, err := checkerdef.GuardDialerOr(ctx, &net.Dialer{}).DialContext(
		ctx, string(h.kind), net.JoinHostPort(targetIP.String(), port))
	if err != nil {
		return h.dialFailure(ctx, err)
	}

	fields := map[string]any{
		jsKeyOK:              true,
		jsKeyRemoteAddr:      conn.RemoteAddr().String(),
		jsKeyIPVersion:       checkerdef.IPVersionOf(targetIP).String(),
		jsKeyConnectDuration: time.Since(start).Milliseconds(),
	}

	return h.adopt(ctx, conn, options, host, fields, start)
}

// dialTunneled hands the RAW host:port to the bastion's dialer and skips local
// resolution entirely: the direct-tcpip request carries the hostname and the
// far side resolves it, which is the whole point for a private name this
// worker's resolver can never see. No remoteAddr and no ipVersion are reported
// — the worker never learns which address the bastion picked, and inventing one
// would be a lie; `tunneled: true` says why they are absent.
func (h *socketHandle) dialTunneled(
	ctx context.Context, dialer checkerdef.ContextDialer, address string, options connectOptions,
) map[string]any {
	// SSH direct-tcpip forwards TCP only. Refusing here, before any dial, is
	// what keeps a tunneled script from silently probing the worker's own
	// network instead of the one behind the bastion.
	if h.kind == socketKindUDP {
		return socketFailure(errUDPCannotBeTunneled)
	}

	if options.ipVersionSet {
		return socketFailure(errIPVersionWithTunnel)
	}

	start := time.Now()

	conn, err := dialer.DialContext(ctx, string(h.kind), address)
	if err != nil {
		return h.dialFailure(ctx, err)
	}

	host, _, splitErr := net.SplitHostPort(address)
	if splitErr != nil {
		host = address
	}

	fields := map[string]any{
		jsKeyOK:              true,
		jsKeyTunneled:        true,
		jsKeyConnectDuration: time.Since(start).Milliseconds(),
	}

	return h.adopt(ctx, conn, options, host, fields, start)
}

// adopt takes ownership of a freshly dialed connection: upgrades it to TLS when
// asked, registers the disposer Execute's defer runs, and completes the fields.
func (h *socketHandle) adopt(
	ctx context.Context,
	conn net.Conn,
	options connectOptions,
	host string,
	fields map[string]any,
	start time.Time,
) map[string]any {
	if options.tls {
		serverName := options.tlsServerName
		if serverName == "" {
			serverName = host
		}

		tlsConn := tls.Client(conn, &tls.Config{
			ServerName: serverName,
			// Opt-in, and exactly the tcp check's `tls_verify: false`.
			InsecureSkipVerify: !options.tlsVerify,
		})

		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()

			return socketFailuref("TLS handshake failed: %v", err)
		}

		state := tlsConn.ConnectionState()
		fields[jsKeyTLS] = map[string]any{
			"version":     checkerdef.TLSVersionString(state.Version),
			"cipherSuite": tls.CipherSuiteName(state.CipherSuite),
		}

		conn = tlsConn

		fields[jsKeyConnectDuration] = time.Since(start).Milliseconds()
	}

	h.conn = conn
	h.runtime.sockets = append(h.runtime.sockets, h.close)

	return fields
}

// dialFailure turns a failed dial into the handle's fields, classifying it the
// way every other checker does so `class` means the same thing everywhere.
func (h *socketHandle) dialFailure(ctx context.Context, err error) map[string]any {
	fields := socketFailuref("connection failed: %v", err)

	if class := checkerdef.ClassifyDialError(err, ctx.Err() != nil); class != "" {
		fields[jsKeyClass] = class
	}

	return fields
}

// close disposes the connection. Idempotent on both paths: the script's own
// close() and Execute's defer.
func (h *socketHandle) close() {
	if h.closed || h.conn == nil {
		h.closed = true

		return
	}

	h.closed = true

	_ = h.conn.Close()
}

// connected reports whether a method has a live connection to act on. A handle
// whose connect failed, and one the script already closed, both answer false —
// so a script that forgot to check `c.ok` fails loudly at the next line rather
// than dereferencing undefined.
func (h *socketHandle) connected() bool {
	return h.conn != nil && !h.closed
}

// toValue builds the JS object: the connect result's fields, plus the methods
// this kind of handle exposes.
func (h *socketHandle) toValue(fields map[string]any) goja.Value {
	obj := h.runtime.vm.NewObject()

	for key, value := range fields {
		_ = obj.Set(key, value)
	}

	writeName, readName := "write", "read"
	if h.kind == socketKindUDP {
		writeName, readName = "send", "receive"
	}

	_ = obj.Set(writeName, func(call goja.FunctionCall) goja.Value {
		return h.runtime.vm.ToValue(
			h.write(call.Argument(0).String(), optionMap(call.Argument(1))))
	})

	_ = obj.Set(readName, func(call goja.FunctionCall) goja.Value {
		return h.runtime.vm.ToValue(h.read(optionMap(call.Argument(0))))
	})

	// Uncounted, like page.close().
	_ = obj.Set("close", func(_ goja.FunctionCall) goja.Value {
		h.close()

		return h.runtime.vm.ToValue(map[string]any{jsKeyOK: true})
	})

	return obj
}

// write sends one payload (TCP) or one datagram (UDP).
func (h *socketHandle) write(data string, opts map[string]any) map[string]any {
	if refusal := h.runtime.spendSocketAction(); refusal != nil {
		return refusal
	}

	if !h.connected() {
		return socketFailure(errSocketNotConnected)
	}

	encoding, _ := opts[optKeyEncoding].(string)

	payload, err := checkerdef.DecodePayload(data, encoding)
	if err != nil {
		return socketFailure(err)
	}

	if len(payload) > maxSocketWriteBytes {
		return socketFailuref(
			"payload of %d bytes exceeds the %d-byte per-call write cap", len(payload), maxSocketWriteBytes)
	}

	ctx, cancel, err := h.runtime.callContext(opts)
	if err != nil {
		return socketFailure(err)
	}

	defer cancel()

	if deadline, ok := ctx.Deadline(); ok {
		_ = h.conn.SetWriteDeadline(deadline)
	}

	start := time.Now()

	sent, err := h.conn.Write(payload)

	fields := map[string]any{
		jsKeyBytes:    sent,
		jsKeyDuration: time.Since(start).Milliseconds(),
	}

	if err != nil {
		h.runtime.panicOnExecutionDeadline()

		fields[jsKeyOK] = false
		fields[checkerdef.OutputKeyError] = err.Error()

		return fields
	}

	fields[jsKeyOK] = true

	return fields
}

// socketReadOptions is the parsed form of read/receive's option map.
type socketReadOptions struct {
	// match is nil for a bare read: return the next chunk the kernel hands
	// over, whatever its size.
	match    func([]byte) bool
	maxBytes int
	encoding string
}

// parseReadOptions validates the read criteria. Exactly one of bytes / until /
// pattern, or none; UDP accepts none of them because a datagram IS the unit.
func parseReadOptions(kind socketKind, opts map[string]any) (socketReadOptions, error) {
	parsed := socketReadOptions{maxBytes: maxSocketReadBytes}

	if opts == nil {
		return parsed, nil
	}

	encoding, _ := opts[optKeyEncoding].(string)
	switch encoding {
	case "", checkerdef.PayloadEncodingText, encodingHex:
		parsed.encoding = encoding
	default:
		return parsed, fmt.Errorf( //nolint:err113 // surfaced verbatim to the script
			"unknown encoding %q, must be one of text, hex", encoding)
	}

	if raw, ok := numericOption(opts[optKeyMaxBytes]); ok {
		parsed.maxBytes = clampReadBytes(int(raw))
	}

	byteCount, hasBytes := numericOption(opts[optKeyBytes])
	until, hasUntil := opts[optKeyUntil].(string)
	patternSrc, hasPattern := opts[optKeyPattern].(string)

	criteria := 0
	for _, present := range []bool{hasBytes, hasUntil, hasPattern} {
		if present {
			criteria++
		}
	}

	if criteria == 0 {
		return parsed, nil
	}

	if criteria > 1 {
		return parsed, errReadCriteriaConflict
	}

	if kind == socketKindUDP {
		return parsed, errUDPReadCriteria
	}

	return applyReadCriterion(parsed, readCriterion{
		byteCount: int(byteCount), hasBytes: hasBytes,
		until: until, hasUntil: hasUntil,
		pattern: patternSrc, hasPattern: hasPattern,
	})
}

// readCriterion is the one accumulate-until rule a read asked for.
type readCriterion struct {
	byteCount  int
	hasBytes   bool
	until      string
	hasUntil   bool
	pattern    string
	hasPattern bool
}

func applyReadCriterion(parsed socketReadOptions, criterion readCriterion) (socketReadOptions, error) {
	switch {
	case criterion.hasBytes:
		if criterion.byteCount <= 0 || criterion.byteCount > checkerdef.MaxPayloadBytes {
			return parsed, fmt.Errorf( //nolint:err113 // surfaced verbatim to the script
				"bytes must be between 1 and %d, got %d", checkerdef.MaxPayloadBytes, criterion.byteCount)
		}

		want := criterion.byteCount
		// The limit IS the request: reading "exactly n" must not stop early at
		// a smaller maxBytes, nor keep accumulating past n.
		parsed.maxBytes = want
		parsed.match = func(buf []byte) bool { return len(buf) >= want }

	case criterion.hasUntil:
		delimiter := []byte(criterion.until)
		parsed.match = func(buf []byte) bool { return bytes.Contains(buf, delimiter) }

	case criterion.hasPattern:
		compiled, err := regexp.Compile(criterion.pattern)
		if err != nil {
			return parsed, fmt.Errorf("invalid pattern: %w", err)
		}

		parsed.match = compiled.Match
	}

	return parsed, nil
}

// clampReadBytes keeps maxBytes inside [1, checkerdef.MaxPayloadBytes].
func clampReadBytes(value int) int {
	if value <= 0 {
		return maxSocketReadBytes
	}

	if value > checkerdef.MaxPayloadBytes {
		return checkerdef.MaxPayloadBytes
	}

	return value
}

// read accumulates from the connection until the requested criterion is met.
func (h *socketHandle) read(opts map[string]any) map[string]any {
	if refusal := h.runtime.spendSocketAction(); refusal != nil {
		return refusal
	}

	if !h.connected() {
		return socketFailure(errSocketNotConnected)
	}

	parsed, err := parseReadOptions(h.kind, opts)
	if err != nil {
		return socketFailure(err)
	}

	// Every byte a script reads — over any handle — comes out of the ONE
	// payload pool an HTTP body also draws from.
	remaining := h.runtime.remainingPayload()
	if remaining <= 0 {
		return socketFailure(errPayloadPoolExhausted)
	}

	limit := parsed.maxBytes
	if int64(limit) > remaining {
		limit = int(remaining)
	}

	ctx, cancel, err := h.runtime.callContext(opts)
	if err != nil {
		return socketFailure(err)
	}

	defer cancel()

	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		// Execute always gives the runtime a deadline, so this only guards a
		// caller that built a jsRuntime by hand — a blocking read with no
		// deadline at all would hang the worker.
		deadline = time.Now().Add(maxTimeout)
	}

	start := time.Now()
	result := checkerdef.ReadUntil(h.conn, deadline, parsed.match, limit)

	// Charged on what came OFF THE WIRE, not on what survived the cap: the
	// budget is "bytes read", and charging only retained bytes would let a
	// script pull unlimited data through repeatedly-capped small reads.
	h.runtime.spendPayload(result.Received)

	fields := map[string]any{
		jsKeyData:     encodeReadData(result.Data, parsed.encoding),
		jsKeyBytes:    len(result.Data),
		jsKeyDuration: time.Since(start).Milliseconds(),
		jsKeyTimedOut: false,
		jsKeyEOF:      false,
	}

	switch result.Outcome {
	case checkerdef.ReadUntilMatched, checkerdef.ReadUntilChunk:
		fields[jsKeyOK] = true
	case checkerdef.ReadUntilCapped:
		fields[jsKeyOK] = false
		fields[checkerdef.OutputKeyError] = fmt.Sprintf(
			"no match in the first %d bytes of the reply", limit)
	case checkerdef.ReadUntilDeadlineFailed:
		fields[jsKeyOK] = false
		fields[checkerdef.OutputKeyError] = fmt.Sprintf(
			"failed to set read deadline: %v", result.Err)
	case checkerdef.ReadUntilStopped:
		h.classifyReadStop(fields, result)
	}

	return fields
}

// classifyReadStop splits a stopped read three ways: the peer closed, MY
// per-call timeout fired, or something else broke. The EXECUTION deadline is
// handled first and does not return at all — it panics, exactly as sleep does,
// which is what lets resultFor report `timeout` rather than a script error.
func (h *socketHandle) classifyReadStop(fields map[string]any, result checkerdef.ReadUntilResult) {
	h.runtime.panicOnExecutionDeadline()

	fields[jsKeyOK] = false

	switch {
	case errors.Is(result.Err, io.EOF):
		fields[jsKeyEOF] = true
		fields[checkerdef.OutputKeyError] = fmt.Sprintf(
			"connection closed by peer after %d bytes", result.Received)
	case isTimeout(result.Err):
		fields[jsKeyTimedOut] = true
		fields[checkerdef.OutputKeyError] = fmt.Sprintf(
			"no matching reply before the call's timeout (%d bytes received)", result.Received)
	default:
		fields[checkerdef.OutputKeyError] = result.Err.Error()

		// A connected UDP socket surfaces an ICMP port-unreachable on the NEXT
		// call, as Go does. Reporting it as `refused` rather than as a generic
		// read error is the difference between "nothing is listening" and "the
		// service is slow".
		if class := checkerdef.ClassifyDialError(result.Err, false); class != "" {
			fields[jsKeyClass] = class
		}
	}
}

// panicOnExecutionDeadline turns "the CHECK ran out of time while this call was
// blocked" into the same panic sleep raises, so resultFor reports `timeout`.
// A per-call `timeout` option expiring is NOT this: that is a value.
func (r *jsRuntime) panicOnExecutionDeadline() {
	if err := r.execCtx.Err(); err != nil {
		panic(r.vm.NewGoError(err))
	}

	if r.executionDeadlineReached() {
		panic(r.vm.NewGoError(context.DeadlineExceeded))
	}
}

// executionDeadlineReached reports whether the check's budget is spent, by the
// CLOCK rather than by the context's timer having fired.
//
// The socket read deadline and the context's own timer are two independent
// clocks armed at the same instant: a read can stop ON the execution deadline a
// hair before the context marks itself done, which would report the check's own
// budget running out as an ordinary per-call timeout VALUE and let the script
// carry on. Reaching the deadline is the fact that matters, so compare against
// it rather than waiting for the timer.
//
// It is shared with resultFor deliberately. Until it was, the two halves
// disagreed about what "deadline reached" means: this path panicked on the
// clock while resultFor classified on `execCtx.Err()`. Go delivers timers late
// under load, so a panic could unwind and be classified while `Err()` was still
// nil, and the check's own budget expiring got reported as
// `script error: GoError: context deadline exceeded` instead of `timeout`.
// That is TestSocketExecutionDeadlineIsATimeout, which passed locally and failed
// on a loaded CI runner. One predicate, one answer.
func (r *jsRuntime) executionDeadlineReached() bool {
	deadline, ok := r.execCtx.Deadline()

	return ok && !time.Now().Before(deadline)
}

// encodeReadData renders received bytes for JS. Default `text` passes them
// through as a string; `hex` is how binary crosses a boundary that cannot carry
// raw bytes.
func encodeReadData(data []byte, encoding string) string {
	if encoding == encodingHex {
		return hex.EncodeToString(data)
	}

	return string(data)
}

// isTimeout reports whether err is a net.Error that timed out.
func isTimeout(err error) bool {
	var netErr net.Error

	return errors.As(err, &netErr) && netErr.Timeout()
}
