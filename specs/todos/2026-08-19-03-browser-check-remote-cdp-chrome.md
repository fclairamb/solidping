---
model: opus
effort: high
---

# Browser check only works where the worker host happens to have Chrome — no container deployment can run it

## Problem

The `browser` check type shells out to a locally-installed Chrome:
[checkbrowser/checker.go:80](../../server/internal/checkers/checkbrowser/checker.go)
builds a `chromedp.NewExecAllocator`, which requires a Chrome/Chromium binary on
the worker host and cold-starts a fresh Chrome process for every execution.

That binary exists nowhere in production:

- The runtime image is `gcr.io/distroless/base-debian13:nonroot`
  ([Dockerfile:136](../../Dockerfile)) — no Chrome, no fontconfig/nss, no shell.
  Every containerized deployment (the k8xp main + checks-us1/eu2 Deployments,
  self-hosted Docker) fails at runtime with the `StatusError` "Chrome/Chromium
  not found" ([checker.go:203](../../server/internal/checkers/checkbrowser/checker.go)).
- Agents (the Fly.io `jp-1` region, customer agents) are single Go binaries;
  they never carry Chrome either.
- Nothing advertises this in advance. The type carries `labelUnsafe` +
  `labelReqChrome` ([checkerdef/types.go:328](../../server/internal/checkers/checkerdef/types.go)),
  but activation is config-driven only
  ([checkerdef/activation.go](../../server/internal/checkers/checkerdef/activation.go)) —
  a user can create a browser check that can never succeed in their region, and
  learns it one failed execution at a time.

In practice the check works on a dev laptop with Chrome installed and nowhere
else. The `browsertosleep` devtool
([devtools/browsertosleep/browsertosleep.go](../../server/internal/devtools/browsertosleep/browsertosleep.go))
exists precisely because in-worker Chrome is expensive and nondeterministic.

Bundling Chrome into the main image is the obvious fix and the wrong one: it
abandons distroless for a ~300–400 MB debian + Chromium + fonts stack, forces
`--no-sandbox` inside the container that holds DB credentials and every org's
secrets (the type is labeled `unsafe` for a reason — it fetches arbitrary
customer URLs), couples Chrome's memory spikes to the Go worker's cgroup, and
still does nothing for agents. It does not actually deliver "works everywhere".

## Proposal

Three additive pieces: a remote CDP allocator as the primary path, the current
exec allocator as fallback, and an honest `browser` worker capability.

### 1. Remote Chrome over CDP (primary)

Add `checkers.browser.cdp_url` (env `SP_CHECKERS_BROWSER_CDP_URL` — note both
`cdp_url` and the sibling `chrome_path` below are multi-word koanf keys and need
the manual env reader treatment like `rate_limiting`). When set, the checker
uses `chromedp.NewRemoteAllocator` against that websocket endpoint instead of
exec-ing a local binary:

- One long-lived browser process, a fresh isolated browser context (incognito)
  + tab per execution, torn down after each check. No per-check process
  cold-start, so the measured duration stops including ~1s of Chrome boot.
- **Concurrency cap: 4** simultaneous browser executions per worker, enforced
  by a semaphore in the checker (both the remote and exec paths). Executions
  past the cap wait on the semaphore inside the check's existing timeout
  budget.
- The browser itself runs in a **separate container**: the
  `chromedp/headless-shell` image (the chromedp project's stripped headless
  build, ~50–70 MB compressed), pinned to a specific tag so every region runs
  the same Chrome version. Deployment shapes: an optional `browser:` service in
  the self-hosted `docker-compose.yml`; in k8xp, a **sidecar per checks-worker
  Deployment** (decided — localhost CDP, lifecycle coupled to the worker; the
  k8s manifests live in the `k8xp` repo, this spec only documents the
  expectation).
- An unreachable/refused CDP endpoint is an infrastructure fault, not a target
  fault: return `StatusError` with an output message naming the fix (mirror the
  existing "Chrome/Chromium not found" style), never `StatusDown`.
- Keep the existing URL-scheme validation
  ([config.go:98](../../server/internal/checkers/checkbrowser/config.go)) — the
  isolation container is defense in depth, not a replacement for it.

### 2. Local exec fallback (unchanged behavior, now configurable)

When no CDP URL is configured, keep the exec allocator: use
`checkers.browser.chrome_path` if set, otherwise probe the usual binary names
(`google-chrome`, `chromium`, `chromium-browser`, chromedp's own default
lookup). This preserves today's zero-config dev-laptop behavior and gives
agents on Chrome-equipped hosts an opt-in path.

Explicitly **no runtime auto-download** of Chromium (the go-rod/playwright
approach): a surprise ~150 MB write and supply-chain exposure on customer
machines, dead in air-gapped deployments.

### 3. `browser` worker capability

Spec 2026-08-15-11 built exactly the right mechanism — the three-state
(`yes`/`no`/`unknown`) self-reported capability registry, designed so a new
capability is a pure string addition
([regions/capabilities.go:86](../../server/internal/regions/capabilities.go)):

- Add `CapabilityBrowser = "browser"` next to `CapabilityIPv4/IPv6` in
  [models/worker.go](../../server/internal/db/models/worker.go) (and to
  `ValidateCapabilitySet`'s known set), re-export it in
  [regions/capabilities.go](../../server/internal/regions/capabilities.go), and
  append it to `aggregatedCapabilities`.
- The worker probes at startup — CDP endpoint answers, or an exec binary is
  found — and includes `browser` in the capability set it already reports on
  heartbeat ([checkworker/worker.go:399](../../server/internal/checkworker/worker.go),
  currently just `egressreport.Current()`). Agents report it through the
  existing additive `capabilities` field in
  [agents/protocol.go:46](../../server/internal/agents/protocol.go); agents
  predating the probe simply stay `unknown`, never `no`.
- **Probe cadence**: the probe result is cached with a **15-minute TTL** and
  re-evaluated lazily when the heartbeat assembles the capability set — no new
  timer, the ~50s heartbeat is the carrier and 15 minutes is the staleness
  ceiling. The probe itself is cheap (a `GET /json/version` on the localhost
  CDP endpoint, or a binary lookup). Two event-driven corrections bypass the
  TTL: a browser execution failing on an **infra error** (CDP unreachable /
  binary missing) invalidates the cache immediately, so the capability drops
  on the next heartbeat rather than up to 15 minutes later; a successful
  execution refreshes it, so recovery is equally prompt.
- Check validation then warns at creation/edit time through the existing
  `CapabilityIndex` path, exactly like the IPv4/IPv6 warning — a region whose
  live workers all lack Chrome says so up front instead of erroring per
  execution.

### Non-goals

- Bundling Chrome into the main image or abandoning distroless.
- Auto-downloading Chromium at runtime on any worker or agent.
- A one-container `-browser` image variant for self-hosters (possible additive
  follow-up; the compose sidecar covers the need first).
- Capability-aware *scheduling/routing*. Parity with IPv4/IPv6 means a
  validation warning only; making the claim path filter on capabilities is a
  separate design.

### Decisions

Resolved with Florent (2026-08-19), folded into the sections above:

- Concurrency cap per worker: **4**, via a semaphore in the checker.
- k8xp topology: **sidecar per checks-worker Deployment**, not a shared
  per-region browser Deployment.
- Capability probe: startup + **15-minute TTL re-probe piggybacked on the
  existing heartbeat**, with immediate cache invalidation when a real browser
  execution fails on an infra error (and refresh on success) — the sidecar
  dying is reflected within one heartbeat, not one TTL.

## Implementation Plan

### 1. Config — `checkers.browser.{cdp_url,chrome_path}`

- `config.CheckersConfig` gains `Browser BrowserCheckerConfig` with `cdp_url` /
  `chrome_path`. Both segments are snake_case, so koanf's env loader cannot
  reach them: add `applyCheckersEnv(&cfg.Checkers)` to `config.Load` and list
  `SP_CHECKERS_BROWSER_CDP_URL` / `SP_CHECKERS_BROWSER_CHROME_PATH` in
  `manualReaderServerEnvVars()` so the startup env check recognizes them.
- Test: `Load()` with the env vars set actually populates the struct (a test
  that only fills the struct would not catch a missing reader).

### 2. `checkbrowser` — remote CDP primary, exec fallback

- `settings.go`: process-wide `Settings{CDPURL, ChromePath}` + `Configure()`,
  called from `checkworker.newCheckWorker` — the one constructor shared by the
  in-process worker and the deported agent.
- `availability.go`: the capability probe (`GET /json/version` on the CDP
  endpoint, else a binary lookup) behind a 15-minute TTL cache with an injected
  clock and prober. `MarkAvailable()` / `MarkUnavailable()` are the two
  event-driven corrections that bypass the TTL.
- `checker.go`:
  - a package-level semaphore of **4**, acquired inside the check's timeout
    budget on BOTH paths; exhaustion returns `StatusTimeout` naming the cap.
  - CDP mode: pre-flight the endpoint (same probe), then
    `chromedp.NewRemoteAllocator` + `chromedp.NewContext(…,
    WithNewBrowserContext())` — a fresh incognito context + tab per execution,
    disposed on cancel. An unreachable endpoint returns `StatusError` with a
    fix-naming message, never `StatusDown`.
  - exec mode (no CDP URL): today's `NewExecAllocator`, plus
    `chromedp.ExecPath` when `chrome_path` is set. No auto-download.
  - outcome feedback: `StatusError` ⇒ `MarkUnavailable()`, up/down ⇒
    `MarkAvailable()`.
  - an unexported `session` seam on `BrowserChecker` lets tests drive a normal
    up/down outcome through the real `Execute` (semaphore + pre-flight) without
    a browser — the positive control for the StatusError test.

### 3. `browser` worker capability

- `models.CapabilityBrowser = "browser"`, re-exported from `regions` and
  appended to `aggregatedCapabilities`.
- `egressreport.Current()` appends `browser` when the probe says yes — but only
  onto a non-nil egress set: appending to a nil ("nothing probed") set would
  turn it into a closed set and render ipv4/ipv6 as a false "no".
- Check validation warns like the IPv6 warning: a new
  `browserRegionWarnings` merged with `ipVersionRegionWarnings` at the three
  existing call sites (validate, create, update).

### 4. Deployment + docs

- Optional `browser:` service (`chromedp/headless-shell`, pinned) in the repo
  `docker-compose.yml` behind a profile, plus the self-hosted compose snippet
  and the sidecar expectation in the docs.
- `web/docs/docs/features/check-types.md` Browser section: CDP mode, sidecar,
  exec fallback, concurrency cap, capability.

### Decided while implementing

- `ValidateCapabilitySet` has **no** known-name allowlist (it validates slug
  shape only), and it must not gain one: the wire format is deliberately
  additive, so a newer agent reporting a capability this server has never heard
  of must still store it. Nothing to add there.
- `labelSafe` gates nothing but `checkers.enabled_labels` activation, and no
  ad-hoc validate/diagnose path executes a checker (`Execute` has exactly one
  caller, the worker). So there is no ad-hoc flow that could fire a real browser
  execution as a side effect, and none is added.
