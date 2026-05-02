# JMAP EventSource — substitute URI template placeholders before connecting

## Symptom

The JMAP supervisor logs a tight reconnect loop with HTTP 400 from the SSE
endpoint:

```
WARN msg="JMAP EventSource connection lost" error="jmap: unexpected HTTP status: SSE 400" backoff_seconds=4
WARN msg="JMAP EventSource connection lost" error="jmap: unexpected HTTP status: SSE 400" backoff_seconds=8
WARN msg="JMAP EventSource connection lost" error="jmap: unexpected HTTP status: SSE 400" backoff_seconds=16
```

The doubling backoff and the wrapped `ErrUnexpectedStatus` come from
`server/internal/jmap/eventsource.go` — `streamOnce` returned status ≥ 300, and
`ListenEventSourceWithReconnect` doubled the backoff on each retry (capped at
5 minutes).

## Root cause

RFC 8620 §7.3 says the `eventSourceUrl` returned in the JMAP session document
is a **URI template** (RFC 6570 Level 1) with three placeholders the client
**MUST** substitute before requesting the URL:

| Placeholder    | Meaning                                                                                |
| -------------- | -------------------------------------------------------------------------------------- |
| `{types}`      | Comma-separated list of types to subscribe to, or `*` for all                          |
| `{closeafter}` | `state` (close after one state event) or `no` (long-lived stream)                      |
| `{ping}`       | Positive integer seconds between server-sent `ping` keepalives, or `0` to disable ping |

A typical Fastmail / Stalwart session advertises something like:

```
https://api.fastmail.com/jmap/event/?types={types}&closeafter={closeafter}&ping={ping}
```

The current code in `server/internal/jmap/eventsource.go:35-53` does **no**
substitution. It just appends `?types=Email` (or `&types=Email`) to the raw
template:

```go
if types != "" {
    separator := "?"
    if strings.Contains(url, "?") {
        separator = "&"
    }
    url += separator + "types=" + types
}
```

So the request that hits the wire is:

```
GET /jmap/event/?types={types}&closeafter={closeafter}&ping={ping}&types=Email
```

The server sees the literal strings `{types}`, `{closeafter}`, `{ping}` as
unparseable parameter values and rejects with **HTTP 400**. Our client wraps it
as `jmap: unexpected HTTP status: SSE 400`, sleeps `backoff`, doubles
`backoff`, retries, gets 400 again — forever (capped at one attempt per
5 minutes once the cap is reached).

The existing tests
(`server/internal/jmap/eventsource_test.go`) use a test server whose
`eventSourceUrl` is `http://host/events` — no placeholders — so they pass
even though the production code path is broken against any conformant JMAP
server.

## Fix

In `server/internal/jmap/eventsource.go`, replace the ad-hoc query-append with
proper template substitution. Keep the function signature stable; widen the
internals.

### 1. New helper: `expandEventSourceURL`

Add a small private helper at the top of the file (or in a new
`eventsource_url.go` if that reads cleaner):

```go
// expandEventSourceURL substitutes the RFC 8620 §7.3 placeholders in the
// discovered eventSourceUrl with the values we want for a long-lived inbox
// listener:
//
//   - {types}      -> the comma-separated list passed by the caller, or "*"
//   - {closeafter} -> "no" (we keep the stream open and reconnect on drop)
//   - {ping}       -> "300" (server sends a ping comment every 5 min so we
//                     can detect dead TCP connections through middleboxes)
//
// If the URL contains none of the placeholders (non-conformant server), it is
// returned unchanged with `?types=...` appended for backward compatibility.
func expandEventSourceURL(raw, types string) string {
    if types == "" {
        types = "*"
    }

    hasPlaceholder := strings.Contains(raw, "{types}") ||
        strings.Contains(raw, "{closeafter}") ||
        strings.Contains(raw, "{ping}")

    if !hasPlaceholder {
        sep := "?"
        if strings.Contains(raw, "?") {
            sep = "&"
        }
        return raw + sep + "types=" + url.QueryEscape(types)
    }

    expanded := raw
    expanded = strings.ReplaceAll(expanded, "{types}", url.QueryEscape(types))
    expanded = strings.ReplaceAll(expanded, "{closeafter}", "no")
    expanded = strings.ReplaceAll(expanded, "{ping}", "300")
    return expanded
}
```

(Add `"net/url"` to the imports.)

### 2. Use it in `ListenEventSourceWithReconnect`

Replace lines 46–53 of `server/internal/jmap/eventsource.go`:

```go
if types != "" {
    separator := "?"
    if strings.Contains(url, "?") {
        separator = "&"
    }

    url += separator + "types=" + types
}
```

with:

```go
url = expandEventSourceURL(url, types)
```

### 3. Tests

Extend `server/internal/jmap/eventsource_test.go` with a focused unit test for
the helper and a regression test for the supervisor path.

**Unit test** for `expandEventSourceURL`:

```go
func TestExpandEventSourceURL(t *testing.T) {
    t.Parallel()
    r := require.New(t)

    cases := []struct {
        name, raw, types, want string
    }{
        {
            name:  "all placeholders substituted",
            raw:   "https://api.example.com/jmap/event/?types={types}&closeafter={closeafter}&ping={ping}",
            types: "Email",
            want:  "https://api.example.com/jmap/event/?types=Email&closeafter=no&ping=300",
        },
        {
            name:  "empty types becomes wildcard",
            raw:   "https://api.example.com/jmap/event/?types={types}&closeafter={closeafter}&ping={ping}",
            types: "",
            want:  "https://api.example.com/jmap/event/?types=%2A&closeafter=no&ping=300",
        },
        {
            name:  "no placeholders falls back to query append",
            raw:   "https://api.example.com/events",
            types: "Email",
            want:  "https://api.example.com/events?types=Email",
        },
        {
            name:  "no placeholders preserves existing query",
            raw:   "https://api.example.com/events?token=x",
            types: "Email",
            want:  "https://api.example.com/events?token=x&types=Email",
        },
        {
            name:  "comma-separated types are escaped",
            raw:   "https://api.example.com/jmap/event/?types={types}",
            types: "Email,Mailbox",
            want:  "https://api.example.com/jmap/event/?types=Email%2CMailbox",
        },
    }

    for _, tc := range cases {
        tc := tc
        t.Run(tc.name, func(t *testing.T) {
            t.Parallel()
            r.Equal(tc.want, jmap.ExpandEventSourceURLForTest(tc.raw, tc.types))
        })
    }
}
```

This requires exposing the helper through the existing
`server/internal/jmap/export_test.go` (add
`var ExpandEventSourceURLForTest = expandEventSourceURL`), keeping it
unexported in the production API.

**Regression test** for the supervisor path: update or add a test in
`eventsource_test.go` that returns an `eventSourceUrl` containing the
placeholders, and have the test server assert the request URL matches the
expanded form (no curly braces left). A simple way:

```go
case "/events":
    if strings.ContainsAny(req.URL.RawQuery, "{}") {
        t.Errorf("server received un-expanded template: %q", req.URL.RawQuery)
        w.WriteHeader(http.StatusBadRequest)
        return
    }
    // ... existing handler body
```

Then change the discovery payload to advertise:

```
"eventSourceUrl":"http://` + req.Host + `/events?types={types}&closeafter={closeafter}&ping={ping}",
```

Both existing tests
(`TestListenEventSourceDispatchesStateEvents`,
`TestListenEventSourceReconnects`) should be migrated to the templated URL so
we don't keep a non-conformant fixture around.

## Out of scope

- Honoring the server's `Last-Event-ID` (we never resume — we always start
  fresh and rely on the post-connect full sync to catch up). Current behavior
  is fine for our use case.
- Switching to `closeafter=state` (would simplify our reconnect loop, since
  the server would close after every state event, but is a behavior change).
- Touching `runPolling`, the `Manager` lifecycle, or any retention logic.
- Tuning the backoff (1s → 5min cap is fine — the spike to 4s/8s/16s in the
  logs is purely a symptom of the bug above).

## Verification

1. `cd server && go test ./internal/jmap/...` — new tests should pass; the
   existing two SSE tests should still pass after their fixtures are updated.
2. `make lint-back` — golangci-lint clean.
3. Restart the running dev server and watch the log: the
   `"JMAP EventSource connection lost"` warnings should stop, replaced by a
   single startup `"starting JMAP EventSource listener"` and `"JMAP session
   discovered"` followed by silence (or `"JMAP sync error"` only on real
   sync failures, not SSE handshake failures).
4. With debug logging on (`LOG_LEVEL=debug ./solidping serve`) and the
   configured Fastmail-style inbox, send a test email to the configured
   address — the `m.syncTrigger` path should fire within a second of the
   email arriving (proving the SSE stream is actually pushing state events,
   not just the polling fallback).

## Honest opinion

This is a real bug but a shallow one. The fix is ~25 lines + tests, and the
diagnosis is unambiguous: the EventSource URL is a URI template, the code
treats it as a plain URL, the JMAP server rejects the result with 400.

Two things are worth flagging beyond the immediate fix:

1. **The unit tests gave false confidence.** Both existing SSE tests use a
   placeholder-free `eventSourceUrl`, so they exercised a code path that
   doesn't exist in production. The regression test above (a server fixture
   that *does* contain placeholders, and asserts none leak through to the
   request) is the more important deliverable than the helper itself —
   without it we'll regress this again the next time someone refactors.
2. **There is no fallback to polling on persistent SSE failure.** Right now,
   if the EventSource endpoint is broken (as it is today), the manager loops
   on it forever and never falls back to `runPolling`, so inbound emails
   would also be invisible until reconnect. That's out of scope for this
   spec, but worth a follow-up: after N consecutive SSE failures, drop into
   the polling path for the rest of the cycle.

---

## Implementation Plan

1. Add an `expandEventSourceURL(raw, types string) string` helper to `server/internal/jmap/eventsource.go` that substitutes `{types}` (with `*` when empty), `{closeafter}` → `no`, `{ping}` → `300`; falls back to query-append when the URL has no placeholders.
2. Replace the existing ad-hoc query-append in `ListenEventSourceWithReconnect` with one call to that helper.
3. Add a unit test for the helper covering: all three placeholders, empty types → wildcard, no-placeholder fallback, existing-query fallback, comma-separated types escaping.
4. Update the existing SSE tests' fixture to advertise an `eventSourceUrl` that contains placeholders, and add a server-side assertion that no curly braces leak through to the actual request.
5. `make lint-back` clean.
