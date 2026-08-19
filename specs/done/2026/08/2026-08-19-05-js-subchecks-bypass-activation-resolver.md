---
model: opus
effort: high
---

# JS sub-checks bypass the check-type activation resolver, running types the operator deactivated

## Problem

`solidping.check(type, config)` inside a `js` check runs **any** check type the
binary was compiled with, regardless of what the operator deactivated in
`checkers.enabled` / `checkers.disabled` / `checkers.enabled_labels`.

The sandbox blocks exactly two types —
[checkjs/checker.go:263](../../server/internal/checkers/checkjs/checker.go)
refuses `js` (no recursion) and `heartbeat` — and then resolves everything else
through the global indirection
[checkjs/checker.go:292](../../server/internal/checkers/checkjs/checker.go) →
[registry/registry.go:52](../../server/internal/checkers/registry/registry.go) →
`GetChecker` + `ParseConfig`, neither of which consults an
`ActivationResolver`. The resolved checker is then executed for real at
[checkjs/checker.go:307](../../server/internal/checkers/checkjs/checker.go).

Concretely, on a server configured with `checkers.disabled: ["browser"]` (or an
`enabled_labels` set that keeps scripting but drops `requires:chrome` —
`browser` carries `unsafe` + `requires:chrome`,
[checkerdef/types.go:328](../../server/internal/checkers/checkerdef/types.go)),
a two-line JS check still starts a real headless Chrome and fetches an
arbitrary customer URL:

```js
return solidping.check("browser", { url: "https://acme.com" });
```

The dashboard says `browser` is disabled — the API's check-type listing is
built from the resolver ([checktypes/service.go:65](../../server/internal/handlers/checktypes/service.go))
— and the check runs anyway. The same bypass applies to every other
deactivated type: `docker` (needs the Docker socket), `kubernetes`, `icmp`
(raw sockets), the database checkers. It is not restricted to the fifteen
typed wrappers registered at
[checkjs/checker.go:241](../../server/internal/checkers/checkjs/checker.go) —
those are convenience aliases; the generic `solidping.check()` entry point
reaches any type name.

### The wider finding: activation is not enforced anywhere

Tracing `ActivationResolver` shows it has exactly **one** construction site,
[app/server.go:786](../../server/internal/app/server.go), and one consumer:
`checktypes.Service` ([checktypes/service.go:60](../../server/internal/handlers/checktypes/service.go)),
which answers the check-type metadata endpoints the dashboard and MCP
`list_check_types` render. `IsTypeEnabled` and `ListEnabledTypes` have **zero
non-test callers** outside `activation.go` itself. Everything on a real path
resolves types through `registry.GetChecker`, which only knows what was
compiled in:

- **Creation** — `CreateCheck`
  ([checks/service.go:1208](../../server/internal/handlers/checks/service.go))
  rejects only types absent from the registry. A deactivated type can be
  created over REST and over MCP `create_check`
  ([mcp/tools_checks.go:202](../../server/internal/mcp/tools_checks.go)).
  `ValidateCheck` ([checks/service.go:410](../../server/internal/handlers/checks/service.go))
  and the YAML import path ([checks/service.go:3375](../../server/internal/handlers/checks/service.go))
  are the same.
- **Claim** — the job-claim SQL is region/lease/lane-scoped with no type
  filter ([checkjobsvc/service.go:454](../../server/internal/checkworker/checkjobsvc/service.go)).
- **Execution** — the worker dispatches via `r.getChecker`
  ([checkworker/worker.go:870](../../server/internal/checkworker/worker.go),
  wired to `registry.GetChecker` at
  [worker.go:259](../../server/internal/checkworker/worker.go)); no activation
  check exists anywhere in `checkworker/`.
- **Agents** — worker `Capabilities`
  ([agents/protocol.go:46](../../server/internal/agents/protocol.go)) are
  `ipv4`/`ipv6`/`browser` region annotations whose own doc comment says they
  "never gate execution"; they are not check types and nothing intersects them
  with activation. `ActivationResolver.WorkerEnabledTypes` from the original
  design was never implemented.

So `checkers.disabled` is, today, a UI-visibility feature: all four enforcement
points named in the original design spec
[2026-03-28-02](../done/2026/03/2026-03-28-02-checker-type-activation.md)
(creation, job reconciliation, claim, execute) are unimplemented. `checkjs` is
the sharpest edge of that — a user-authored script picks the type at runtime,
so no amount of creation-time gating would close it — but it is one symptom,
not the whole defect. Per-org overrides are equally inert: the handler passes
`nil` for `orgDisabled` with a "follow-up" comment
([checktypes/handler.go:34](../../server/internal/handlers/checktypes/handler.go)).

### Why the resolver is not simply reachable from `checkjs`

`Checker.Execute(ctx, config)`
([checkerdef/interface.go:51](../../server/internal/checkers/checkerdef/interface.go))
carries no server config and no org identity, and `newJSRuntime`
([checkjs/checker.go:142](../../server/internal/checkers/checkjs/checker.go))
holds only `execCtx` + `*JSConfig`. So the resolver has to be threaded in.
Two facts constrain the choice:

- `checkjs` cannot import `registry` (import cycle) — hence the existing
  `ResolveChecker` package global. Whatever is added should follow that
  established indirection rather than invent a third pattern
  (`checkfreeboxline.ConnectionResolverFunc` and
  `checkkubernetes.ClientsetResolverFunc`, wired at
  [app/server.go:344](../../server/internal/app/server.go) and following, are
  the same shape).
- The JS runtime has no org identity, so **org-level** disabling
  (`IsTypeEnabled`'s `orgDisabled` argument) cannot be honored without
  threading the org through `Execute`. Server-level activation — the operator's
  deactivation, which is what the report is about — is reachable today.

This is pre-existing and was deliberately recorded as out of scope by spec
[2026-08-19-03](../done/2026/08/2026-08-19-03-browser-check-remote-cdp-chrome.md)
(see the "Pre-existing, OUT OF SCOPE" note in its Implementation Plan); the
browser-CDP work neither introduced nor changed it.

## Proposal

Make the JS sub-check path refuse a check type that is deactivated at the
server level, with an error the script author can actually read.

### 1. A shared server-level activation gate

Add a package-level hook in `checkjs`, mirroring the existing `ResolveChecker`
indirection:

```go
// TypeEnabled reports whether a check type is enabled at the server level.
// nil means "no gate configured" — every type is allowed (unit tests,
// standalone use of the checker package).
var TypeEnabled func(checkerdef.CheckType) bool
```

Wire it where the **worker** is constructed, not only in the HTTP server's
route setup. `CheckWorker` already holds the full `*config.Config`
([checkworker/worker.go:114](../../server/internal/checkworker/worker.go)) and
both `NewCheckWorker` and `NewAgentCheckWorker` funnel through `newCheckWorker`
([checkworker/worker.go:212](../../server/internal/checkworker/worker.go)), so
one wiring point covers the embedded worker **and** the standalone agent.
Wiring only at [app/server.go:786](../../server/internal/app/server.go) — the
current sole construction site, which lives in HTTP route setup and is not
reached by an agent process at all — would leave the gate `nil` exactly where
checks actually execute, i.e. the bypass intact. Build the resolver once from
`cfg.Checkers` and share it rather than constructing two that can drift.

Whether the gate is expressed as a `func`, as a `*checkerdef.ActivationResolver`
value, or by making the existing `ResolveChecker` return `false` for
deactivated types is an implementation call. The constraint is that the
**refusal reason must be distinguishable from "unknown check type"** — folding
it into `ResolveChecker`'s existing `ok=false` would tell a user that `browser`
does not exist, which is both false and unhelpful.

### 2. Refuse, don't silently run

In `jsRuntime.check`
([checkjs/checker.go:262](../../server/internal/checkers/checkjs/checker.go)),
after the `js`/`heartbeat` block and before `ResolveChecker`, reject a
deactivated type the same way the existing guards do — a result map with
`status: "error"` and an explicit message, e.g.:

> `check type "browser" is disabled on this server`

Deliberately **not** a Go error from `Execute`: the existing sub-check guards
(recursion block, sub-check limit, unknown type, invalid config) all return an
error-shaped result map so the script can inspect it and keep running. A
refused sub-check should behave identically. The wording must say *disabled*,
not *not allowed* / *unknown*, so an operator reading a failing check's output
recognizes their own configuration as the cause.

Also decide and record: does the refusal count against the `maxSubChecks` budget
([checkjs/checker.go:274](../../server/internal/checkers/checkjs/checker.go))?
Recommendation: leave the existing ordering alone (the counter increments
first) rather than reshuffle the guards — but state the choice in the code
comment so it is not read as an accident.

### 3. Scope decisions to make explicitly

The audit finding is the JS bypass; the surrounding gap is that activation is
enforced nowhere. Fixing the whole design at once (creation + reconciliation +
claim + execute, plus per-org overrides) is a much larger change with real
migration questions — what happens to *existing* check rows of a type an
operator later disables — and is **not** what this spec asks for. The
deliverable here is the JS path. But the choice must be deliberate:

- **In scope, required:** `solidping.check(...)` refuses a server-deactivated
  type.
- **In scope if it falls out naturally, with its own decision note and test:**
  the worker's own `getChecker` dispatch
  ([worker.go:870](../../server/internal/checkworker/worker.go)). Placing the
  gate at the worker's resolution chokepoint would cover both callers at once,
  which is tidier than a JS-only special case — but it changes behavior for
  deployments whose stored checks predate a later `checkers.disabled` edit
  (those checks start erroring instead of running). If that is taken, the
  resulting result status/output must be a clear "disabled by server
  configuration", not a generic error, and the migration consequence must be
  stated. If it is left out, say so explicitly in the Implementation Plan
  rather than silently.
- **Explicitly out of scope:** creation-time and claim-time enforcement, and
  per-org `orgDisabled` overrides. Record them as follow-ups; do not
  half-implement them.
- **Server-level only.** The JS runtime has no org identity, so `IsTypeEnabled`
  is called with an empty `orgDisabled`. Say so in a comment at the gate, so
  the next reader does not assume org overrides are honored.
- **Agent semantics.** A standalone agent process reads its **own**
  `checkers.*` config, not the control plane's — the agent path never sees
  server-side activation ([agentws/handler.go:464](../../server/internal/handlers/agentws/handler.go)
  stores capabilities, not types). Wiring the gate in `newCheckWorker` means an
  agent enforces the config of the host it runs on, which is defensible (that
  host's operator is the one who owns its Chrome/Docker socket/raw sockets) but
  is a semantic choice — state it. An agent left at defaults enables
  everything, so the JS bypass survives there unless its config says otherwise;
  that limitation belongs in the Implementation Plan, not buried.
- **Do not weaken the existing `js`/`heartbeat` block.** It must keep refusing
  those two types even when they are enabled.

### 4. Tests

In `server/internal/checkers/checkjs/` (and/or `registry/`, where the real
resolver is wired — the current tree has **no** test exercising the sub-check
path at all, so the harness has to be built):

- **Negative:** with the gate configured to deactivate a type, a script calling
  `solidping.check("<deactivated>", {...})` gets an error result whose message
  names the type and says it is disabled — and, critically, the sub-checker's
  `Execute` is **never entered**. Assert that with a stub checker that records
  invocation (or a counter), not merely by matching the error string: the
  string alone would pass even if the check ran and failed afterwards.
- **Positive control:** with the same gate, an *enabled* type requested from
  the same script still resolves and executes normally — proving the gate is
  selective and did not just break all sub-checks.
- **Nil-gate control:** with `TypeEnabled` unset, behavior is unchanged (every
  type resolves as today), so the checker package stays usable in isolation.
- **Wiring test:** assert the gate is actually set by the real construction
  path, not just settable. A test that only pokes the package global proves
  nothing about production — this is exactly how the bug survived.

## QA

```
make build-backend lint-back test
```

Never `make build` / `make ci`.

## Implementation Plan

### D1 — Shape of the gate: a `func` package global in `checkjs`

`checkjs` cannot import `registry` (import cycle), which is why `ResolveChecker`
already exists as a package global. The gate follows the same shape:

```go
type TypeEnabledFunc func(checkType checkerdef.CheckType) bool
var TypeEnabled TypeEnabledFunc
```

`nil` means **no gate configured — every type is allowed**, so the checker
package stays usable standalone and in unit tests.

A `func` rather than a `*checkerdef.ActivationResolver` value: `checkjs` has no
org identity to pass as `IsTypeEnabled`'s second argument, so exposing the
resolver type here would advertise an org dimension the runtime cannot fill.
The `func` states exactly the question the JS path can answer.

**Not** folded into `ResolveChecker`'s existing `ok=false`: that path already
means "unknown check type", and telling an operator that `browser` does not
exist when they disabled it themselves is both false and unhelpful. The refusal
must be distinguishable, so it is a separate hook with its own message.

### D2 — Wiring point: `newCheckWorker`, not `app/server.go`'s route setup

The gate is installed in `checkworker.newCheckWorker`
(`server/internal/checkworker/worker.go`), the single constructor **both**
`NewCheckWorker` (embedded worker) and `NewAgentCheckWorker` (standalone agent)
funnel through — exactly where `checkbrowser.Configure` is already installed for
the same reason. `CheckWorker` holds the full `*config.Config`, so
`cfg.Checkers` is in hand.

Wiring only at `app/server.go:786` (the resolver's current sole construction
site) would have left the gate `nil` in an agent process, which never executes
that HTTP route-setup code — i.e. the bypass intact exactly where checks run.

**On "build the resolver once".** The resolver is built once per process *role*:
`newCheckWorker` builds exactly one and the gate closure captures it (no second
resolver, and no unused struct field); `app/server.go` keeps building its own
for `checktypes.Service` because route setup runs before `startCheckWorker` and
the two are not reachable from one another. They cannot drift: both are
`checkerdef.NewActivationResolver(&cfg.Checkers)` — the same constructor, a pure
function of the same `cfg.Checkers` value. A cross-reference comment is added at
both sites so a future edit to one is visibly an edit to the other.

### D3 — Refusal shape: an error-shaped result map, not a Go error

The refusal is returned from `jsRuntime.check` as
`{status: "error", output: {error: "check type \"browser\" is disabled on this server"}}`,
identical in shape to the four guards already there (recursion block, sub-check
limit, unknown type, invalid config). The script can inspect it and keep
running, which is the established contract; a Go error out of `Execute` would
kill the whole script and break that contract.

Wording says **disabled on this server**, deliberately not "not allowed"
(reserved for the `js`/`heartbeat` sandbox rule) and not "unknown" (reserved for
a type absent from the registry), so an operator reading a failing check's
output recognises their own `checkers.*` configuration as the cause.

### D4 — `maxSubChecks` accounting: the refusal DOES consume budget

The gate is placed **after** the `subCheckCount.Add(1)` limit guard, so a
refused sub-check counts against the 20-call budget. This keeps the existing
guard ordering untouched (a script cannot spin the refusal path for free either).
Recorded in a code comment at the gate so it does not read as an accident.

### D5 — Scope

**In scope (required, done):** `solidping.check(type, config)` — and therefore
the fifteen typed wrappers, which all funnel through the same `check()` — refuses
a check type deactivated at the server level.

**Deliberately LEFT OUT: the worker's own `getChecker` dispatch**
(`checkworker/worker.go:870`). It did not fall out naturally, and taking it is a
behaviour change with a migration consequence the spec did not ask for: any
stored check whose type an operator disables *later* would immediately start
producing error results instead of running, on every deployment that already has
such rows, with no migration path and no warning at config-load time. That is a
product decision (disable-then-error vs. disable-then-hide), not a bug fix, and
it belongs with the creation-time/claim-time work below where the whole
lifecycle can be designed at once. Recorded as a follow-up, not silently
skipped.

**Explicitly out of scope, recorded as follow-ups (not half-implemented):**

- Creation-time enforcement (`CreateCheck` / `ValidateCheck` / YAML import /
  MCP `create_check`).
- Claim-time enforcement (the job-claim SQL has no type filter).
- Worker-execution enforcement (`getChecker`, above).
- Per-org `orgDisabled` overrides — `checktypes/handler.go` still passes `nil`.
- `ActivationResolver.WorkerEnabledTypes` from the original design spec.

### D6 — Server-level only (no org overrides)

The JS runtime has no org identity: `Checker.Execute(ctx, config)` carries
neither server config nor an org, and `jsRuntime` holds only `execCtx` +
`*JSConfig`. So the gate calls `IsTypeEnabled(checkType, nil)` — an **empty**
`orgDisabled`. A per-org disable is therefore **not** honoured inside a JS
sub-check. A comment at the gate says exactly this so the next reader does not
assume org overrides are enforced.

### D7 — Agent semantics

A standalone agent reads its **own** `checkers.*` configuration, not the control
plane's (the agent protocol carries `ipv4`/`ipv6`/`browser` capabilities, not
check types, and those explicitly never gate execution). Wiring the gate in
`newCheckWorker` therefore means **an agent enforces the configuration of the
host it runs on**. That is the defensible reading — the operator of that host
owns its Chrome, its Docker socket and its raw sockets — but it is a semantic
choice, not a neutral one.

Consequence to be explicit about: **an agent left at defaults enables every
type, so the JS bypass survives on that agent** unless its own config disables
the type. Closing that would require propagating the control plane's activation
set to agents (`WorkerEnabledTypes`), which is in the follow-up list.

### D8 — The `js`/`heartbeat` sandbox block is untouched

It runs first and keeps refusing those two types with its existing "not allowed
in JS scripts" wording, even when both are enabled at the server level. The new
gate is additive.

### D9 — Tests (the harness does not exist yet)

`checkjs` has no test exercising the sub-check path at all, so it is built here,
in `server/internal/checkers/checkjs/subcheck_gate_test.go`:

1. **Negative** — with the gate deactivating a type, `solidping.check(...)`
   returns an error result naming the type and saying it is *disabled*, **and a
   stub checker's `Execute` is never entered** (asserted via an invocation
   counter, not by string match alone — the string would pass even if the check
   ran and failed afterwards).
2. **Positive control** — the same gate lets an *enabled* type resolve and
   execute normally, from the same script, proving selectivity.
3. **Nil-gate control** — with `TypeEnabled` unset, every type resolves exactly
   as before.
4. **Sandbox control** — `js`/`heartbeat` stay refused even with the gate
   enabling everything.
5. **Typed-wrapper control** — `solidping.icmp({...})` is refused too, pinning
   that the fifteen aliases share the one gated entry point.
6. **Distinguishability control** — a type absent from the resolver still
   reports *unknown check type*, never *disabled* (D1).
7. **Budget control** — 21 refused calls hit the sub-check limit, pinning D4.

Plus a **wiring test** (`server/internal/checkworker/activation_wiring_test.go`)
asserting the gate is
installed by the **real** construction path (`newCheckWorker`, reached from both
`NewCheckWorker` and `NewAgentCheckWorker`) — poking the package global proves
nothing about production, which is precisely how this bug survived.

Because these tests mutate package globals they run sequentially
(`//nolint:paralleltest`, with the reason stated) and restore the previous value
via `t.Cleanup`.

### QA

`make build-backend lint-back test` only — never `make build` / `make ci`.
