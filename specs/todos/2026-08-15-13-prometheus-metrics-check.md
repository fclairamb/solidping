---
model: opus
effort: high
---

# Add a Prometheus metrics check type (scrape or PromQL, warning/critical thresholds)

## Problem

All 40 existing check types answer "is it up?" — none can see a service that
answers 200 while dying: a queue backing up, a disk filling, an error rate
climbing. Many targets already expose the answer as a Prometheus `/metrics`
endpoint (or sit behind a Prometheus server), but teams without a
Prometheus + Alertmanager stack have no way to alert on those values.
SolidPing already has everything needed to close that gap: distributed HTTP
probing, a graded `StatusWarning`/`StatusDown` model
([types.go](../../server/internal/checkers/checkerdef/types.go) — Warning
counts as up, aggregates to Degraded, no incident), per-result `Metrics`
that aggregate and graph over time, and the warning/critical two-tier
threshold pattern just shipped for `domain`/`ssl`
([config.go](../../server/internal/checkers/checkdomain/config.go)
`WarningDays`/`CriticalDays`).

## Proposal

Add a `prometheus` check type: fetch one numeric value — either by scraping
a `/metrics` endpoint or by running a PromQL query against a Prometheus
server — and grade it against warning/critical thresholds.

### Config shape

```json
{
  "url": "https://app.example.com/metrics",
  "mode": "scrape",
  "metric": "process_open_fds",
  "labels": { "instance": "app-1" },
  "operator": ">",
  "warningValue": 800,
  "criticalValue": 1000,
  "match": "single",
  "onMissing": "down",
  "headers": { "Authorization": "Bearer …" }
}
```

- **`mode`** — `scrape` (default): `GET url`, parse the Prometheus text
  exposition format, select series by `metric` name + optional `labels`
  (exact-match subset). `promql`: `url` is the Prometheus server base URL;
  run `GET {url}/api/v1/query?query={query}` with a `query` config field
  instead of `metric`/`labels`; accept scalar and instant-vector results
  (matrix → config/eval error). `promql` is also the escape hatch for
  `rate()` over counters — v1 does no client-side rate computation (that
  needs state between executions; explicitly out of scope).
- **Series selection** — `match` decides what happens when the selector
  matches more than one series: `single` (default, error result — forces an
  unambiguous selector), or `min`/`max`/`sum`/`avg` to aggregate.
- **Threshold semantics** — the check fires when `value <operator>
  threshold` is true: critical breached → `StatusDown` (paging), else
  warning breached → `StatusWarning` (amber, counts as up), else
  `StatusUp`. One shared `operator` (`>`, `>=`, `<`, `<=`, `==`, `!=`);
  at least one of `warningValue`/`criticalValue` required. Validation
  mirrors `validateThresholds` ordering logic: for `>`/`>=` require
  `criticalValue >= warningValue`, for `<`/`<=` the reverse; `==`/`!=`
  accept `criticalValue` only (a warning tier is meaningless there).
  Thresholds are float64 and 0 is a legal threshold, so "is it set" needs
  explicit presence tracking (pointers or a set-flag from `FromMap`), not
  the zero-value convention `checkdomain` uses for days.
- **`onMissing`** — status when the metric/series doesn't exist or the
  PromQL result is empty: `down` (default — an absent metric usually means
  the target is broken), `warning`, or `up`.
- **`headers`** — optional map for auth (metrics endpoints are routinely
  behind bearer/basic auth), same shape as the HTTP check's headers.

### Backend

1. New package `server/internal/checkers/checkprometheus/` (checker +
   config + tests), registered in the hardcoded switch in
   [registry.go](../../server/internal/checkers/registry/registry.go) and
   in `checkTypesRegistry`
   ([types.go:291](../../server/internal/checkers/checkerdef/types.go)):
   `{Type: CheckTypePrometheus, Labels: [labelSafe, labelStandalone,
   labelCatInfrastructure], Description: "Alert on Prometheus metric
   thresholds", SupportsTunnel: true}`. It is a plain HTTP GET, so route it
   through the context dialer like checkhttp to honor `SupportsTunnel`.
2. Parse the exposition format with `github.com/prometheus/common/expfmt`
   (already in the dependency tree via `client_golang`, which serves our
   own `/metrics`). Gauge/counter/untyped read directly; histogram/summary
   families are addressed through their flattened series (`_sum`, `_count`,
   `_bucket{le=…}`, `{quantile=…}`) so no special casing in the selector.
3. Result shape: `Metrics: {"value": <float64>}` so the value aggregates
   and graphs like any latency metric — this is half the feature: the check
   page becomes a time-series graph of the monitored value. `Output`:
   resolved metric/query, matched-series labels, matched count, the
   threshold + operator that fired (or "within thresholds"), and mode.
4. Errors: unreachable/non-200/parse failure → `StatusDown` with the error
   in Output (consistent with other checkers); config problems
   (`Validate`) rejected up front — url required, mode-appropriate fields
   required (`metric` in scrape, `query` in promql), operator from the
   allowed set.
5. Default slug (`prometheus`) and sample configs for the
   `registry.GetAllSampleConfigs` feed
   ([service.go:69](../../server/internal/handlers/checktypes/service.go))
   — one scrape sample, one promql sample (recall the checkdnsbl/checksip
   audit gap: samples and default slug are part of "done").
6. Tests, table-driven per `server/CLAUDE.md`, with `httptest` fakes for
   both modes: value within / breaching warning / breaching critical for
   `>` and `<` operators; `==`/`!=` single-threshold; multi-series with
   `match: single` (error) and each aggregation; missing metric ×
   `onMissing` values; empty promql vector; scalar promql result; matrix
   result rejected; label subset matching; headers actually sent
   (positive control); context cancellation.

### Frontend (dash0)

7. New form type registered in
   [index.ts](../../web/dash0/src/components/checks/form/types/index.ts),
   fields in `infra.tsx` (alongside SNMP/Docker/Kubernetes): url, mode
   select toggling the scrape fields (`metric`, label pairs) vs the promql
   `query` textarea; operator select + warning/critical numeric inputs;
   `match`, `onMissing`, `headers` behind the Advanced section. Build from
   the design-reference primitives; mobile-usable per repo conventions.
8. Playwright coverage for create/edit round-trip of both modes
   (author-only if the local devloop can't run E2E).

### Docs

9. New section in
   [check-types.md](../../web/docs/docs/features/check-types.md) — under a
   new `## Metrics` heading (it's the first check that inspects a value
   rather than a service), documenting both modes, threshold semantics
   with a `<`-operator example (free-disk style), `onMissing`, and the
   counter/rate caveat pointing at promql mode.

### Open questions

- `SupportsIPVersion`: checkhttp pins the address family on its transport;
  same trick applies here trivially. Leaning: include it if it falls out of
  reusing checkhttp's transport helper, otherwise defer.
- Warning-only checks (no `criticalValue`) can never page — intended
  (matches how warningDays works elsewhere), just confirming.
- Whether `scrape` mode should cap response size (a huge /metrics dump on
  every execution) — leaning: yes, a few MB limit with a clear error.

## Resolved open questions

Answered 2026-08-16. These are directives — implement them as written.

> `SupportsIPVersion`: … Leaning: include it if it falls out of reusing
> checkhttp's transport helper, otherwise defer.

**Decision:** Confirmed as leaning. Include `SupportsIPVersion` **only** if it
falls out of reusing checkhttp's transport helper cleanly. If the helper does
not transfer without contortions, defer it and say so in the final report — do
not build a bespoke IP-version path for this checker.

> Warning-only checks (no `criticalValue`) can never page — intended, just
> confirming.

**Decision:** Confirmed and intended. A config with only `warningValue` is
valid and can never produce `StatusDown`. This matches how `warningDays`
behaves on `domain`/`ssl`. Do **not** require `criticalValue`.

> Whether `scrape` mode should cap response size … leaning: yes, a few MB
> limit with a clear error.

**Decision:** Yes — cap the scrape response body at **5 MB**. Exceeding it is
a `StatusDown` result whose `Output` names the cap explicitly (so the operator
knows it was a limit, not a parse failure), not a silent truncation — a
truncated exposition body would parse into wrong values, which is worse than
an error. The limit applies to `scrape` mode; `promql` responses are bounded
by the query itself. Add a test proving a body over the cap is refused rather
than partially parsed.
