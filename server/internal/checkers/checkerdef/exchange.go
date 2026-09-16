package checkerdef

import (
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"time"
)

// Output/metric keys produced by an Exchange.
const (
	OutputKeyReceivedData  = "received_data"
	OutputKeyBytesSent     = "bytes_sent"
	OutputKeyBytesReceived = "bytes_received"
	OutputKeyReadCount     = "read_count"
)

// Exchange is the single send-then-wait-for-the-answer conversation the `tcp`
// and `udp` checkers perform. Their send/read/match blocks were near-identical
// (and near-identically wrong); this is the one implementation.
//
// The whole exchange runs under ONE deadline — the context deadline the check's
// `timeout` produced — rather than re-arming `now + timeout` at the dial, the
// write and the read, which let a `timeout: 10s` check run for 30 s.
type Exchange struct {
	// Send is the decoded payload, empty when the check only listens.
	Send []byte
	// ExpectData is the decoded substring the reply must contain.
	ExpectData []byte
	// ExpectPattern is the compiled RE2 the reply must match.
	ExpectPattern *regexp.Regexp
	// Timeout is the configured timeout, reported verbatim in the timeout message.
	Timeout time.Duration
	// Deadline bounds the write and every read.
	Deadline time.Time
}

// NewExchange decodes a checker's send/expect configuration into a runnable
// Exchange. The encodings and the pattern were already accepted by `Validate`,
// so an error here means a config that never went through it.
func NewExchange(
	sendData, sendEncoding string,
	expectData, expectEncoding string,
	expectPattern string,
	timeout time.Duration,
	deadline time.Time,
) (*Exchange, error) {
	send, err := DecodePayload(sendData, sendEncoding)
	if err != nil {
		return nil, fmt.Errorf("send_data: %w", err)
	}

	expect, err := DecodePayload(expectData, expectEncoding)
	if err != nil {
		return nil, fmt.Errorf("expect_data: %w", err)
	}

	exchange := &Exchange{
		Send:       send,
		ExpectData: expect,
		Timeout:    timeout,
		Deadline:   deadline,
	}

	if expectPattern != "" {
		pattern, compileErr := regexp.Compile(expectPattern)
		if compileErr != nil {
			return nil, fmt.Errorf("expect_pattern: %w", compileErr)
		}

		exchange.ExpectPattern = pattern
	}

	return exchange, nil
}

// HasExpectation reports whether anything at all is asserted about the reply.
// Without one, the check can only prove the port accepted a connection.
func (e *Exchange) HasExpectation() bool {
	return len(e.ExpectData) > 0 || e.ExpectPattern != nil
}

// Matches reports whether the accumulated reply satisfies every expectation.
// It runs on the FULL buffer, never on the 1 KB-capped output copy.
func (e *Exchange) Matches(buf []byte) bool {
	return matchesExpectation(buf, e.ExpectData, e.ExpectPattern)
}

// Run writes the payload and then reads until the expectation is satisfied, the
// 4 KB cap is reached, the peer closes, or the deadline passes. It fills
// `metrics` and `output` in every outcome and returns nil when the exchange
// succeeded (including the no-expectation, diagnostics-only case), or the
// failure Result to return as-is.
//
// Over UDP each Read is one datagram; datagrams accumulate in the same buffer
// and are matched the same way, so a reply split across two datagrams works.
//
// The loop itself lives in ReadUntil, shared with the JS runtime's socket
// handles; what stays here is the only part that is an Exchange's business —
// what each of the loop's exits MEANS for a check's verdict.
func (e *Exchange) Run(conn net.Conn, metrics, output map[string]any) *Result {
	if len(e.Send) > 0 {
		if failure := e.write(conn, metrics, output); failure != nil {
			return failure
		}
	}

	// Nothing sent and nothing expected: a bare port probe, no read phase.
	if len(e.Send) == 0 && !e.HasExpectation() {
		return nil
	}

	// No expectation means the old `send_data`-only semantics: ONE best-effort
	// read for diagnostics, and no failure on silence. That is exactly
	// ReadUntil's `match == nil` mode.
	var match func([]byte) bool
	if e.HasExpectation() {
		match = e.Matches
	}

	read := ReadUntil(conn, e.Deadline, match, MaxExchangeReadSize)

	if read.Outcome == ReadUntilDeadlineFailed {
		output[OutputKeyError] = fmt.Sprintf("failed to set read deadline: %v", read.Err)

		return &Result{Status: StatusError, Metrics: metrics, Output: output}
	}

	e.report(metrics, output, read.Data, read.Received, read.Reads)

	if !e.HasExpectation() {
		return nil
	}

	switch read.Outcome {
	case ReadUntilMatched:
		return nil
	case ReadUntilCapped:
		output[OutputKeyError] = fmt.Sprintf(
			"no match in the first %d bytes of the reply", MaxExchangeReadSize)

		return &Result{Status: StatusDown, Metrics: metrics, Output: output}
	case ReadUntilStopped, ReadUntilChunk, ReadUntilDeadlineFailed:
		return e.readFailure(read.Err, metrics, output, read.Received)
	default:
		return e.readFailure(read.Err, metrics, output, read.Received)
	}
}

func (e *Exchange) write(conn net.Conn, metrics, output map[string]any) *Result {
	if err := conn.SetWriteDeadline(e.Deadline); err != nil {
		output[OutputKeyError] = fmt.Sprintf("failed to set write deadline: %v", err)

		return &Result{Status: StatusError, Metrics: metrics, Output: output}
	}

	sent, err := conn.Write(e.Send)
	if err != nil {
		output[OutputKeyError] = fmt.Sprintf("failed to send data: %v", err)

		return &Result{Status: StatusDown, Metrics: metrics, Output: output}
	}

	metrics[OutputKeyBytesSent] = sent
	output[OutputKeyBytesSent] = sent

	return nil
}

// readFailure classifies the end of the read loop. Silence behind an open port
// is the one thing this check exists to catch, so it gets its own status
// instead of a generic Down with a read error.
func (e *Exchange) readFailure(err error, metrics, output map[string]any, received int) *Result {
	if !e.HasExpectation() {
		return nil
	}

	switch {
	case errors.Is(err, io.EOF):
		output[OutputKeyError] = fmt.Sprintf(
			"connection closed after %d bytes without a matching reply", received)

		return &Result{Status: StatusDown, Metrics: metrics, Output: output}
	case isTimeoutError(err):
		output[OutputKeyError] = fmt.Sprintf(
			"no matching reply within %s (%d bytes received)", e.Timeout, received)

		return &Result{Status: StatusTimeout, Metrics: metrics, Output: output}
	default:
		output[OutputKeyError] = fmt.Sprintf("failed to read response: %v", err)

		return &Result{Status: StatusDown, Metrics: metrics, Output: output}
	}
}

func (e *Exchange) report(metrics, output map[string]any, buf []byte, received, reads int) {
	metrics[OutputKeyBytesReceived] = received
	output[OutputKeyBytesReceived] = received
	output[OutputKeyReadCount] = reads
	output[OutputKeyReceivedData] = RenderReplyData(buf)
}

func isTimeoutError(err error) bool {
	var netErr net.Error

	return errors.As(err, &netErr) && netErr.Timeout()
}
