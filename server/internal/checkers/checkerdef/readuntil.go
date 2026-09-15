package checkerdef

import (
	"net"
	"time"
)

// ReadUntilOutcome says how a ReadUntil loop ended. It exists so the caller can
// decide what each ending MEANS — the same exit is a `down` for a `tcp` check
// with an expectation and a perfectly good bare read for a JS socket handle.
type ReadUntilOutcome int

const (
	// ReadUntilMatched is the happy path: match reported the accumulated buffer
	// satisfies the caller.
	ReadUntilMatched ReadUntilOutcome = iota
	// ReadUntilCapped is the byte limit reached without a match. Data holds the
	// capped buffer.
	ReadUntilCapped
	// ReadUntilStopped is a read error — peer closed (io.EOF), the deadline
	// fired, the connection was reset. Err carries it, Data the partial buffer.
	ReadUntilStopped
	// ReadUntilChunk is the `match == nil` ending: exactly one read was
	// performed and whatever the kernel handed over is in Data.
	ReadUntilChunk
	// ReadUntilDeadlineFailed means the read deadline could not even be armed —
	// our own failure, not the target's. Err carries it and nothing was read.
	ReadUntilDeadlineFailed
)

// ReadUntilResult is what one accumulate-until-match read loop observed.
type ReadUntilResult struct {
	// Data is the accumulated buffer, capped at the caller's limit.
	Data []byte
	// Received is the total number of bytes read off the wire, INCLUDING the
	// ones dropped past the limit. It is what a diagnostic should report.
	Received int
	// Reads is how many non-empty reads it took.
	Reads int
	// Outcome names which of the loop's exits was taken.
	Outcome ReadUntilOutcome
	// Err is set for ReadUntilStopped and ReadUntilDeadlineFailed.
	Err error
}

// ReadUntil accumulates from conn until match is satisfied, limit bytes have
// been buffered, the peer closes, or the deadline passes.
//
// It is the ONE read-until-match implementation in the codebase: Exchange.Run
// (the `tcp` and `udp` checkers' single send-then-wait conversation) and the JS
// runtime's socket handles both call it, so the cap semantics and the
// timeout/EOF classification cannot drift apart between them.
//
// `match == nil` means "one read": exactly one Read is performed and its result
// returned, which is both the `send_data`-with-no-expectation diagnostic read
// and a JS `c.read()` with no criteria.
//
// The ordering of the three exits is load-bearing and deliberately preserved
// from the loop this was extracted from: a read that both satisfies the match
// AND errors counts as matched, and one that errors while filling the buffer to
// the cap counts as stopped rather than capped.
func ReadUntil(conn net.Conn, deadline time.Time, match func([]byte) bool, limit int) ReadUntilResult {
	res := ReadUntilResult{}

	if err := conn.SetReadDeadline(deadline); err != nil {
		res.Outcome = ReadUntilDeadlineFailed
		res.Err = err

		return res
	}

	buf := make([]byte, 0, limit)
	chunk := make([]byte, limit)

	for {
		n, err := conn.Read(chunk)
		if n > 0 {
			res.Reads++
			res.Received += n

			if room := limit - len(buf); n > room {
				n = room
			}

			buf = append(buf, chunk[:n]...)
		}

		res.Data = buf

		if match != nil && match(buf) {
			res.Outcome = ReadUntilMatched

			return res
		}

		if err != nil {
			res.Outcome = ReadUntilStopped
			res.Err = err

			return res
		}

		if len(buf) >= limit {
			res.Outcome = ReadUntilCapped

			return res
		}

		if match == nil {
			res.Outcome = ReadUntilChunk

			return res
		}
	}
}
