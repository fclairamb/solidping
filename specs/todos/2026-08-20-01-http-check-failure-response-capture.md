---
model: opus
effort: high
---

# HTTP checks discard the failing response — capture it as incident diagnostics, opt-in

## Problem

When an HTTP check fails, the only evidence that survives is a status code and
an error string in the result `Output` map
([checkerdef/types.go:94](../../server/internal/checkers/checkerdef/types.go)).
The response the probe actually received — the 502 page, the WAF block screen,
the maintenance notice, the JSON error payload — is **already fully in memory**
at failure time: the checker reads the body up to `maxBodySize` for
`body_expect`/`body_pattern` matching
([checkhttp/checker.go:409](../../server/internal/checkers/checkhttp/checker.go))
and then throws it away.

So the user opening an incident three days later knows *that* the check failed,
but not *what the probe saw* — which is the single most useful diagnostic for
an HTTP failure, and the difference between "origin is down" and "the CDN is
serving a challenge page to our probe".

The screenshots idea spec
([specs/ideas/2026-01-05-screenshots.md](../ideas/2026-01-05-screenshots.md))
routes HTTP checks through a post-hoc headless-Chrome render. For HTTP checks
that is the weakest possible evidence (a fresh browser, seconds later, possibly
after the blip passed) and drags in a Chrome dependency the check itself never
needed. This spec is the replacement for that phase: textual capture of the
actual failing response, at the actual failure moment, at near-zero marginal
cost. Browser-check screenshots remain a separate feature and are **out of
scope** here.

## Proposal

### Check option

Add to `HTTPConfig` ([checkhttp/config.go:61](../../server/internal/checkers/checkhttp/config.go)),
following the existing snake_case config-key convention (`body_expect`, …):

```json
{ "url": "https://example.com", "capture_failure_response": true }
```

Opt-in, default `false` — response bodies can contain PII or session material,
so the org decides per check. Expose the toggle in the dash0 HTTP check form,
config-as-code, and the OpenAPI spec.

### What gets captured, and when

Only on **failed** executions, and only when the option is set. The capture is
built from data the checker already holds — no second request, no extra I/O:

- status line (e.g. `HTTP/2 503 Service Unavailable`);
- response headers, **redacted**: values of `Set-Cookie`, `Authorization`,
  `Proxy-Authenticate`, and any header matching `*token*`/`*key*`/`*secret*`
  are replaced with `[redacted]`. Request headers are never captured — they
  carry the check's own credentials;
- the body, truncated to a hard cap (16 KiB) with a `truncated: true` marker
  and the original `Content-Length`;
- binary guard: if the body is not valid UTF-8 or the content type is not
  text-like (`text/*`, JSON, XML, HTML), store only metadata (content type,
  size, SHA-256) — never raw bytes;
- capture metadata: timestamp, region/agent that ran the probe, resolved IP if
  available.

Total bounded at ~20 KiB per capture by construction, so no storage quota or
entitlement work is needed.

### Transport — a dedicated field, not the Output map

Carry the capture in a new optional `diagnostics` field on the checker result
struct ([checkerdef/types.go:94](../../server/internal/checkers/checkerdef/types.go))
and on the agent WS `result` frame next to `output`
([agents/protocol.go:80](../../server/internal/agents/protocol.go)),
`omitempty` in both directions so old agents keep working unchanged and new
agents talking to an old server lose nothing but the capture.

**Do not put it in `Output`.** `Output` is persisted per result row as JSONB
([models/result.go:136](../../server/internal/db/models/result.go)); a flapping
30 s check would write ~2,880 × 16 KiB/day of raw rows that the aggregation
job deletes 24 h later anyway. The capture must be invisible to the results
table entirely.

### Persistence — on incident transitions only

The incident pipeline (`ProcessCheckResult`,
[incidents/service.go:186](../../server/internal/handlers/incidents/service.go))
already sees the result at the moment it decides state transitions. Persist the
capture into `incidents.details` JSONB
([models/incident.go:56](../../server/internal/db/models/incident.go)):

- on `createIncident` ([service.go:697](../../server/internal/handlers/incidents/service.go))
  → `details.failureResponse` — the response that opened the incident;
- on reopen → overwrite `failureResponse` (the new onset is the relevant
  evidence);
- every other failure's capture is dropped on the floor — it only ever existed
  in memory and on the wire.

This mirrors the eager-capture / lazy-persist decision from the screenshots
spec, and keys the evidence on the object the user actually opens later (the
incident survives; raw results are reaped after 24 h).

### UI

On the incident detail page, a "What the probe saw" section: status line,
headers table, collapsed body viewer (`<pre>`, monospace, scrollable), with the
capture timestamp and probing region displayed **next to the content** — the
capture is what the probe received at failure detection, and must never be
labelled as anything more. Truncation and binary-body cases get an explicit
notice. Follow the design reference for the collapsible/table primitives.

**Never public.** The capture is operational evidence for the org, not
status-page content. Audit that the public status-page and subscriber payloads
never serialize `incidents.details` (and add a regression test pinning that),
since response bodies may contain internal hostnames, stack traces, or PII.

### Testing

- Checker: capture present only on failure + option set; truncation at the
  cap; header redaction list; binary/no-UTF-8 guard; no capture when the
  request itself failed before a response existed (timeout, DNS, TLS — capture
  degrades to absent, the existing error output already covers those).
- Incident service: persisted on open and reopen, dropped on non-transition
  failures and on successes; results table rows never contain the capture.
- Protocol: round-trip with the field absent (old agent) and present.
- Public-surface regression: status-page API responses contain no
  `failureResponse`.

## Open questions

- **Refresh while open?** A long incident's cause can evolve (503 → challenge
  page). v1 keeps only the onset capture; a rate-limited "latest" slot
  (e.g. refresh at most every 15 min) is a possible follow-up, not in scope.
- **Interaction with the outage-vs-incident split**
  ([specs/backlog/2026/05/2026-05-08-04-outage-vs-incident-split.md](../backlog/2026/05/2026-05-08-04-outage-vs-incident-split.md)):
  when the split lands, this capture is *outage*-side operational evidence and
  must move with it — one more reason the public-surface guard above is a hard
  requirement now.
- **Other probe-style checks** (gRPC status + message, SMTP banner, TCP
  first-bytes) could adopt the same `diagnostics` field later; the transport
  and persistence are deliberately checker-agnostic, but only HTTP is in scope.
