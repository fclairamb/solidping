# Rate limiter: data race on `ipEntry.lastSeen`

## Context
Surfaced by `-race` during E2E runs of the 2026-06-10 dashboard/UI batch (two
independent subagents flagged it: specs `-03` and `-05`).

`server/internal/middleware/ratelimit.go` keeps per-IP state in a `sync.Map`
(`RateLimiter.entries`). `sync.Map` makes the **map operations** safe, but the
stored value (`*ipEntry`) has a plain `lastSeen time.Time` field that is
accessed concurrently **without synchronization**:

- **Write**: `getEntry()` sets `entry.lastSeen = time.Now()` on every request
  (`ratelimit.go:79`), from request-handling goroutines.
- **Read**: `cleanupLoop()` reads `entry.lastSeen.Before(cutoff)`
  (`ratelimit.go:130`), from the background eviction goroutine (every 2m).

`time.Time` is a multi-word struct (wall/ext/loc), so an unsynchronized
concurrent read/write is a true data race — Go's race detector reports it at
`ratelimit.go:79`.

Real-world impact is low (worst case: an entry evicted slightly early/late, or
a torn `lastSeen` read), but it is a genuine race that trips `-race` and can
destabilize parallel test runs.

## Goal
Make `lastSeen` access race-free with a minimal, well-contained change, and add
a `-race` regression test so it can't silently come back.

## Behaviour
- Replace the plain `lastSeen time.Time` field with an atomically-accessed value
  — e.g. `lastSeen atomic.Int64` holding Unix-nanos, with helper accessors —
  or guard the field with a small mutex on `ipEntry`. Prefer the atomic-int
  approach (lock-free, hot path is the per-request write).
- Update `getEntry()` (write) and `cleanupLoop()` (read) accordingly.
- No behavioural change to rate-limiting / concurrency-limiting semantics.

## Out of scope
- The already-fixed timeout-middleware header race (resolved in spec `-05`).
- Any rework of the rate-limiter algorithm, eviction interval, or config.

## Testing
- A focused unit test that hammers `getEntry()` from many goroutines while the
  cleanup logic reads `lastSeen`, run under `go test -race`, must be clean.
- `make build` + `make test` (with `-race` where the suite supports it) green.
