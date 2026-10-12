---
model: sonnet
effort: high
---

# HTTP results record only a total duration, so a slow check cannot be split into DNS, connect, TLS, server and download time

## Problem
An HTTP result carries one number: `Duration`, taken right after `client.Do` returns the headers (`server/internal/checkers/checkhttp/checker.go:408-409`). When a check gets slower, nothing says whether DNS, the TCP connect, the TLS handshake, the server or the body transfer is to blame.

Most of the plumbing exists:
- An `httptrace.ClientTrace` is attached to every probe (`checker.go:165-166`). `connFamilyTracker` (`checker.go:852-984`) only uses it for the IP family, the dialed address and how far the request got.
- `checkerdef.Result.Metrics` is meant for this (`server/internal/checkers/checkerdef/types.go:142`). HTTP never sets it.
- gRPC records `dns_time_ms`, `connect_time_ms`, `tls_time_ms` and writes no key for a phase that did not run (`server/internal/checkers/checkgrpc/dial.go:113-133`). TCP records `connection_time_ms` and `tls_handshake_time_ms` (`server/internal/checkers/checktcp/checker.go:219,257`).
- Rollups keep arbitrary metrics. `aggregateMetrics` averages every float metric by name (`server/internal/jobs/jobtypes/job_aggregation.go:657-689`, rules in `determineMetricAggregation` `:616-654`) and stores the map on the rollup row (`:1146-1183`). Hour/day/month rows can carry phase averages with no schema change.

Connection reuse breaks the breakdown today. The default transport is a shared, pooled HTTP/1.1 transport with a 90 s idle timeout (`server/internal/checkers/checkerdef/httptransport.go:84-100`), and the docs say connections are reused between probes that succeed (`web/docs/docs/features/check-types.md:55-58`). A check running every 60 s rides a reused connection, so DNS, connect and TLS would be missing on most results and present only when the pool expired. HTTP/2 probes already get a fresh transport with keep-alives off (`checkerdef/httpprotocol.go:68-78`).

## Proposal
1. Add a `phaseTimer` in a new `server/internal/checkers/checkhttp/timing.go`. Guard it with a mutex like `connFamilyTracker`: dial hooks fire on other goroutines. Attach it next to the family tracker in `execute()` (`checker.go:165`); `httptrace.WithClientTrace` composes both traces. Keys, float ms, same conversion as gRPC's `durationMs` (`checkgrpc/checker.go:390`):
   - `dns_time_ms`: DNSStart to DNSDone. Absent for an IP literal or a tunneled dial.
   - `connect_time_ms`: first ConnectStart to first successful ConnectDone. Happy Eyeballs races count as wall time.
   - `tls_time_ms`: TLSHandshakeStart to TLSHandshakeDone without error. Absent on `http://`.
   - `ttfb_time_ms`: WroteRequest to GotFirstResponseByte, the server's time as seen by the client.
   - `download_time_ms`: end of headers to end of the body read at `checker.go:519-524`. Present only when the probe reads the body (assertions, failure capture, health). Timed in `executeRequest` and passed back through `responseInfo`.
   - `redirect_time_ms`: probe start to the GetConn of the final hop. Present only when at least one redirect was followed.
   A phase that did not happen writes no key, never 0 (gRPC rule).
2. Redirects: reset the per-hop timestamps at each GetConn, so dns/connect/tls/ttfb/download are the final hop's. `redirect_time_ms` covers the earlier hops. `Duration` stays the total of all hops.
3. Fresh connection per probe: set `req.Close = true` on the first request (`checker.go:300-306`) and on each hop inside `CheckRedirect` (`checker.go:342`). The transport stays shared (no per-probe transport to leak), but no connection is reused, so every result has a full breakdown. This is a behaviour change: checks running more often than every 90 s now include connect and TLS in their response time. Say so in the commit body and the changelog entry (`wiki/conventions/changelog.md`), and rewrite the "Connections are still reused" sentence in `check-types.md:55-58`.
4. Write the metrics on every HTTP result in `execute()` after `executeRequest` returns (`checker.go:168-196`), down and timeout results included: a probe that timed out after a 5 s DNS lookup should show it. Leave `Duration` where it is (`checker.go:409`), so `download_time_ms` sits outside response time. Document that.
5. Health checks are out of scope. `checkhealth` builds a new Result without `httpResult.Metrics` (`server/internal/checkers/checkhealth/checker.go:78-82`) and sets its own at `:130`. Leave it.
6. API: metrics already come back on the result detail, on `LastResult.metrics` and on list results with `with=metrics`. List the HTTP keys in the `metrics` descriptions of `server/internal/app/openapi/openapi.yaml:11325` (LastResult) and `:12731` (OrgResult). No new endpoint.
7. Result detail: for HTTP results, render a "Timing" card before the generic metrics list in `web/dash0/src/routes/orgs/$org/checks.$checkUid.results.$resultUid.tsx:349-364`. One horizontal bar per phase in order (redirect, dns, connect, tls, ttfb, download) with its ms value; missing phases are not drawn. Reuse it in the last-result block of `checks.$checkUid.index.tsx:1798-1810`. `data-testid` on the card and each row (`http-timing-card`, `http-timing-phase-dns`, ...). Strings via `t()` in en/fr/de/es. Add the component to `design-reference.tsx`.
8. Phase chart: on the check detail page, add a "Timing" view beside the response time chart (`checks.$checkUid.index.tsx:1593`): stacked areas of the per-bucket phase averages, raw and rollup rows alike. It needs `with=metrics`, which the default chart plan never requests on purpose (`web/dash0/src/components/checks/chart-fetch-params.test.ts:175-178`, spec 2026-08-22-04 §3). Fetch it with its own query only while the Timing view is selected; the default plan stays blob-free and that test stays as is. Keep the view in the URL search with `replace: true` (`web/dash0/AGENTS.md`). HTTP checks only.
9. Docs: a "Timing breakdown" subsection in the HTTP section of `web/docs/docs/features/check-types.md`, before `### TCP` (line 221): each key, when it is absent, the final-hop rule, that download is outside response time, and the fresh-connection behaviour.

## Tests
- `server/internal/checkers/checkhttp/timing_test.go` (new), all `t.Parallel()` and `require`:
  - `httptest.NewServer` reached as `http://localhost:<port>`: `dns_time_ms`, `connect_time_ms`, `ttfb_time_ms` present and >= 0, `tls_time_ms` absent, dns + connect + ttfb <= `Duration`.
  - `httptest.NewTLSServer` with `verifySsl: false`: `tls_time_ms` present and >= 0, same sum rule.
  - With a `bodyAssertions` config, `download_time_ms` is present; with no body-reading option it is absent.
  - Server A redirects to server B: `redirect_time_ms` present; dns/connect/ttfb are B's (B sleeps 50 ms, so `ttfb_time_ms` >= 50 while A answers at once).
  - Two `Execute` calls in a row on the shared transport: the second result still has `connect_time_ms` (proves the fresh connection; positive control for the absence cases).
  - Negative: connection refused on a closed `127.0.0.1` port. Result down, and no `dns_time_ms`, `connect_time_ms`, `tls_time_ms`, `ttfb_time_ms`, `download_time_ms` keys.
  - Negative: server sleeps past the timeout. `connect_time_ms` present, `ttfb_time_ms` absent.
- `server/internal/jobs/jobtypes/job_aggregation_test.go`: rolling up results where only some carry `tls_time_ms` averages the present values only.
- dash0 unit test for the timing card: phase order, missing phases not drawn.
- `web/dash0/e2e/check-http-timing.spec.ts`: a test-mode HTTP check's result page shows `http-timing-card`; selecting the Timing view writes the search param and renders the chart.
- `make test`, `make lint`, `bun run lint:e2e && bun run typecheck:e2e`.

## To verify
- Whether quic-go's HTTP/3 transport fires any httptrace hook. If not, `httpVersion: "3"` results carry no phase keys; document it.
- Whether the egress guard's transport (`httptransport.go:126`) resolves through a resolver that fires DNSStart/DNSDone with the request context. If not, `dns_time_ms` is absent under an enforcing policy; document it.
- Day and month rollups average the hourly averages without weighting (`aggregateAvg`, `job_aggregation.go:760`). Fine for a chart; confirm the day tier is built from hour rows.

## Open questions
- Fresh connection per probe, or keep pooling? Recommended: fresh connection (step 3). With pooling, dns/connect/tls show up only when the pool expired, which depends on the check period, and TLS slowness never reaches the response time. The cost is one handshake per probe on the target.
- For a redirected probe, the final hop's phases or the sum over all hops? Recommended: final hop plus `redirect_time_ms`. Summing TLS time across different hosts describes no real connection.
- Time the download when nothing needs the body (read and discard up to 10 MB)? Recommended: no. It adds bandwidth to every probe for a number no assertion uses. Download stays absent when the body is not read.
