# Docker check — restart-loop (crash-loop) detection

## Context

Inspired by the Maintenant competitor analysis ([`docs/competitors/maintenant.md`](../../docs/competitors/maintenant.md)),
which lists **restart-loop detection** as one of its container-observability strengths.
SolidPing's Docker check today reports container state and health but does **not** flag a
crash-looping container that is *technically running*:

- `buildResult` reads `info.RestartCount` into `metrics["restartCount"]`
  ([`checkdocker/checker.go:156`](../../server/internal/checkers/checkdocker/checker.go)) and
  `info.State.StartedAt` into `output["startedAt"]`
  ([`checkdocker/checker.go:149`](../../server/internal/checkers/checkdocker/checker.go)).
- Status is `StatusDown` only when the container is **not running**
  ([lines 158-167](../../server/internal/checkers/checkdocker/checker.go)) or **unhealthy**
  ([lines 169-178](../../server/internal/checkers/checkdocker/checker.go)). A container that
  restarts every 20s but is "running" at inspect time reports **`StatusUp`**.

Two architectural facts that shape this:

1. **`RestartCount` is cumulative for the container's lifetime**, and a single Docker
   `ContainerInspect` is a point-in-time snapshot. One inspect cannot, by itself, tell you the
   restart *rate* — only the lifetime total and when the current run started.
2. **Checkers are stateless and run on distributed workers.** `Execute(ctx, config)` receives
   only the config ([`checkerdef` interface](../../server/internal/checkers/checkerdef/interface.go)) —
   no previous result, no per-check memory. The worker returns a `Result`; history lives
   server-side in the `results` table (`metrics.restartCount` is persisted per raw row).
3. **There is no "warning" / "degraded" status** — only `Up/Down/Timeout/Error`
   ([`checkerdef/types.go:13-17`](../../server/internal/checkers/checkerdef/types.go)).

## My honest opinion

"Detect a restart loop" sounds simple but the architecture forces a real choice, so here it is.

A restart *loop* is fundamentally a **rate** ("≥ K restarts in the last N minutes"). The clean
way to measure a rate is a delta over time — which we cannot get from one stateless inspect.
There are three viable approaches:

- **A — checker-local heuristic (no history).** Inside `buildResult`, flag a loop when the
  container is `Running` **and** it (re)started very recently (`now - StartedAt <= window`)
  **and** `RestartCount >= minRestarts`. Rationale: an *actively* looping container always shows
  a freshly-recent `StartedAt` at inspect time, while a long-stable container does not — so the
  recency guard is what actually carries the signal, and the count floor stops the first
  legitimate restart (a deploy) from tripping it.
  - **Pro:** fits the stateless-worker model exactly, zero schema, zero plumbing, flows through
    the existing status → incident pipeline for free.
  - **Con:** heuristic. A single deploy restart on a container with a high *lifetime* count can
    false-positive for **one** interval (it self-clears next check, because `StartedAt` ages out
    of the window). A brand-new container looping from birth is caught only once its lifetime
    count crosses `minRestarts` (a few cycles of lag).

- **B — server-side delta over `results` history (accurate).** On Docker-result ingest, compare
  the new `restartCount` to the most recent prior Docker result for that check within `window`;
  if `delta >= K`, it's a loop. True rate, reuses already-persisted data.
  - **Pro:** accurate; no false positive on a single restart.
  - **Con:** lives in the generic result-ingest path; status is already decided by the worker, so
    raising a loop signal means **either** mutating status post-hoc (awkward, and there's no
    warning status) **or** raising an incident independently of status — more plumbing, and it
    blurs the "checker decides status" boundary.

- **C — thread previous state into the job (accurate, in-checker).** Pass the last known
  `restartCount` to the worker via the job context so the checker computes the delta locally.
  - **Pro:** keeps the decision in the checker, accurate.
  - **Con:** new plumbing to carry prior state into `check_jobs`/job config for one check type.

**My recommendation: ship A as the MVP, scope B as the accurate V2.** A delivers the feature
the competitor has and fits SolidPing's architecture with the least risk. A flapping container
*is* "unhealthy but (intermittently) up" — exactly what the new `StatusWarning` state
(from [`2026-06-20-04`](2026-06-20-04-status-warning-degraded.md)) is for: current status shows
amber **Warning** (rolled-up periods show **Degraded**), it counts as up for availability, and
doesn't page or manufacture a false outage. So a *detected loop on a
currently-running container* → `StatusWarning`; a container that's actually not-running/unhealthy
still → `StatusDown` (and pages) as today. The one-interval false positive on a deploy is real
but self-healing, and because warning doesn't page, a stray flag costs nothing. If A's accuracy
proves insufficient, B is the correct upgrade (scoped below). The A-vs-B and Warning-vs-Down calls
are decisions for you (below).

## Goals

- A Docker check whose container is *running* but **crash-looping** is detected and surfaced.
- Detection is **configurable and opt-in** (thresholds in check config; disabled by default so
  existing Docker checks don't change behaviour).
- The loop is **observable** in the result output (`restartLoop: true`, `restartCount`,
  `secondsSinceStart`) so the dashboard can explain *why* the check is degraded.
- No DB migration for the MVP (approach A).

## Dependency

- **[`2026-06-20-04` (first-class `StatusWarning`)](2026-06-20-04-status-warning-degraded.md)
  lands first** (assuming the recommended Warning outcome). It supplies the `StatusWarning` value
  (current status Warning; aggregated Degraded) and its availability/incident semantics. If you
  choose `StatusDown` for loops instead (open
  question 2), this dependency drops and the change is self-contained.

## Out of scope

- Approach B/C (true rate via history or threaded state) — the accurate V2, scoped here but not
  built. Pick this up if A's heuristic proves too noisy.
- The rest of Maintenant's container observability (live CPU/mem/net/disk, log streaming, image
  update detection, Compose grouping) — deliberately **not** pursued; that's a different product
  surface (see the Maintenant analysis "takeaways").

## Design (Approach A — MVP)

### Config (`checkdocker/config.go`)

```go
type DockerConfig struct {
    Host          string        `json:"host,omitempty"`
    ContainerName string        `json:"containerName,omitempty"`
    ContainerID   string        `json:"containerId,omitempty"`
    Timeout       time.Duration `json:"timeout,omitempty"`
    // Restart-loop detection (opt-in). RestartLoopMinRestarts == 0 → disabled.
    RestartLoopMinRestarts int           `json:"restartLoopMinRestarts,omitempty"` // e.g. 3
    RestartLoopWindow      time.Duration `json:"restartLoopWindow,omitempty"`      // e.g. 120s
}
```

Defaults: detection **off** unless `restartLoopMinRestarts > 0`. When enabled and
`restartLoopWindow == 0`, default to `120s`. Validate window `<= maxTimeout`-style sane bound and
`minRestarts >= 0`.

### Execution (`checkdocker/checker.go`)

In `buildResult`, after the existing not-running / unhealthy checks pass (container is running
and healthy), add:

```go
if cfg.RestartLoopMinRestarts > 0 && info.State.Running {
    started, _ := time.Parse(time.RFC3339Nano, info.State.StartedAt)
    sinceStart := time.Since(started)
    output["secondsSinceStart"] = sinceStart.Seconds()
    if info.RestartCount >= cfg.RestartLoopMinRestarts && sinceStart <= cfg.RestartLoopWindow {
        output["restartLoop"] = true
        output["error"] = fmt.Sprintf(
            "restart loop suspected: %d restarts, last start %s ago",
            info.RestartCount, sinceStart.Round(time.Second))
        // StatusWarning per 2026-06-20-04 (current=Warning, rolls up to Degraded);
        // use StatusDown if you chose to page.
        return &checkerdef.Result{Status: checkerdef.StatusWarning, ...}
    }
}
```

`buildResult` needs access to `cfg` (currently it doesn't receive it) — pass the resolved config
in, or pass the two thresholds. `metrics["restartCount"]` stays as today; add
`output["secondsSinceStart"]` always (cheap, useful) so the dashboard can show flap context even
when not yet over threshold.

## Implementation

1. **`checkdocker/config.go`**: add the two fields, `FromMap` parsing (string duration like the
   existing `timeout`), `GetConfig`, validation.
2. **`checkdocker/checker.go`**: thread config into `buildResult`; parse `StartedAt`; add the
   loop branch + `secondsSinceStart` output.
3. **Frontend (`web/dash0`)**: Docker check config form exposes the two fields (advanced
   section); check-detail surfaces `restartLoop` / `restartCount` / `secondsSinceStart`. Verify
   against the design reference and reuse existing primitives.
4. **Docs**: update the Docker check section of [`docs/api-specification.md`](../../docs/api-specification.md)
   and any container feature page under `docs/features/`.

## Open questions / decisions for the user

1. **Approach A (heuristic, this spec) vs B (server-side delta over history)?** A ships fast and
   fits the architecture; B is accurate but more invasive and needs an incident-without-status
   path. Default in this spec is A.
2. **Loop ⇒ `StatusWarning` (current Warning, Degraded in rollups; no page — recommended, depends
   on `2026-06-20-04`) or `StatusDown` (pages)?** Spec assumes Warning: a flapping-but-running
   container is "unhealthy but up," and warning shows amber without a false outage. Choose Down if
   a crash-loop should page immediately (then this spec has no dependency on `2026-06-20-04`).
3. **Default thresholds** when enabled: `minRestarts = 3`, `window = 120s` — reasonable for
   typical backoff loops? Loops with long backoff (minutes) would need a wider window.

## Verification

- **Unit (table-driven, `testify/require`, `t.Parallel()`):** drive `buildResult` with synthetic
  `container.InspectResponse` values:
  - Running, `RestartCount >= min`, `StartedAt` within window → `StatusWarning`, `restartLoop=true`.
  - Running, high `RestartCount` but `StartedAt` **older** than window → `StatusUp` (proves the
    recency guard; the deploy-with-old-high-count case self-clears).
  - Running, recent start but `RestartCount < min` → `StatusUp` (proves the count floor).
  - Detection disabled (`minRestarts == 0`) → behaviour byte-for-byte as today.
  - Not-running / unhealthy still short-circuit to `StatusDown` before the loop branch.
- **Manual:** run a container with `--restart=always` and a command that exits immediately
  (`sh -c 'exit 1'`); point a Docker check at it with detection enabled; confirm the current
  status flips to Warning (amber) with `restartLoop` and the reason in output, uptime is
  unaffected, and a rolled-up period shows Degraded.
- `make lint` / `make test`; `make test-dash` if the form changes.

## Files referenced

- `server/internal/checkers/checkdocker/config.go` — restart-loop config fields
- `server/internal/checkers/checkdocker/checker.go` — detection in `buildResult`
- `server/internal/checkers/checkerdef/types.go` — `StatusWarning` returned for loops (added by `2026-06-20-04`)
- `web/dash0/src/routes/**` — Docker config form + check-detail output
- `docs/api-specification.md`, `docs/features/**` — config + output shape
- `docs/competitors/maintenant.md` — source of the requirement
