---
model: opus
effort: high
---

# The `repair: auto` host grant is dropped at run time when the script or env holds a `${…}` reference

## Problem

Found by the security scan of 2026-10-25 (finding S1).

Severity: medium. It is a bypass of the R1 fix (spec `specs/done/2026/10/2026-10-24-01-ai-repair-auto-apply-partial-runtime-host.md`, commit `08c3aa21`, on this branch and not yet in `main`).
- **Who can trigger it:** the same as R1 and P1. A third party who controls content the repair loop reads, with no SolidPing account.
- **What the target org needs:** an AI-authored JS check that holds secrets and has `ai.repair: auto`. The model has to follow the injected instructions.
- **Also inert without any attacker:** every auto-repaired check whose stored `env` uses a `${param:…}`, `${env:…}` or `${var:…}` reference is never held to its grant.

The R1 fix holds a version `repair: auto` applied to the hosts the previous version reached. It stores two values in the `ai` block: `repair_hosts`, and `repair_grant`, which is `RepairDigest(script, env)` (`checkers/checkjs/config/ai.go:71-89`).

At run time:
- `JSConfig.RepairAllowlist` (`checkers/checkjs/config/config.go:195-201`) honours the grant only when `RepairDigest(c.Script, c.Env)` equals it.
- `JSChecker.Execute` (`checkers/checkjs/checker.go:157-162`) installs the allowlist only when `RepairAllowlist` reports `held`.

The config `Execute` receives is not the stored one:
- `CheckWorker.materializeConfig` (`checkworker/worker.go:1019-1063`, called at `:1188`) runs `secretref.ResolveConfig` over **every** string of the job config, `script` and `env` included.
- It applies the `${param:…}` overlay and expands `${var:…}` and allowed `${env:…}` references (`secretref.Pattern`, `secretref/secretref.go:43`; `ExecutionResolverWith`, `secretref/resolvers.go:157-168`).
- `${var:uuid}` / `${var:timestamp}` always resolve and need no parameter (`secretref/vars.go:110-117`), and they change on every run.
- The agent path resolves the same way: the param overlay is applied in `handlers/agentws/handler.go:1220`, then `secretref.ResolveConfig` runs in `checkworker/backend/ws_tunnel.go:53`.

So whenever the stored script or env contains a resolvable reference, the digest computed at run time differs from the stored grant. `RepairAllowlist` returns `false`, and the run has the egress guard only, as before R1. The grant is computed at apply time over the stored text (`aichecks/repair.go`, `repairedConfig` with `RepairDigest(candidate, target.cfg.Env)`), so `NormalizeConfig` (`config.go:206-244`) keeps it on the stored row. Nothing reports the mismatch.

The static half of the R1 fix does not close this:
- `hiddenHost` (`aichecks/guards.go:371-388`) is regex-based. `netCallRE` (`guards.go:350-352`) only sees a network API written as a direct call (`.get(`, `solidping.x(`, `fetch(`, `](`…).
- A call made through an alias of the same API is not a call site for it. So the candidate also passes the "no more call sites than before" count and `CheckSameAPIs`.
- The commit says as much: the grant is "the boundary".

**Scenario:**
1. A drift failure queues `ai_repair`.
2. Injected page text makes the model write a candidate that does three things:
   - it keeps the previous literal host;
   - it adds a request to a host built at runtime that carries `secrets.<NAME>`, through an aliased API and behind a condition that is false during the loop and the verification run (as in R1);
   - it adds a `${var:…}` reference anywhere in the script text, for example in a comment.
3. The loop and verification runs go through the AI runner, which does not resolve references and holds its own allowlist, so they pass. Every gate passes and `applyRepair` writes the version with a grant.
4. On schedule, `materializeConfig` expands the reference, the digest no longer matches, and the version runs unheld. Once the condition holds, the secret goes to the attacker's public host. No `aiRepairHostRefused` result is produced, so `RevokeAutoRepair` never fires.

Impact: the same as R1, a persistent secret leak on every scheduled run, which R1 was meant to close.

## Proposal

Goal: whether a scheduled run is held must depend on the **stored** config, never on the text after reference resolution.

1. **Recommended: decide `held` on the stored config, before resolution.**
   - Write paths already drop a stale grant through `NormalizeConfig` (`config.go:206-244`). At run time, the presence of a grant is then enough: `RepairAllowlist` honours any non-empty `repair_grant` together with its `repair_hosts`, and stops recomputing the digest over the run's config.
   - Keep the digest check in `NormalizeConfig` only, where it runs against the stored text.
   - This needs every check write path to run `NormalizeConfig` (see To verify). If one does not, fall back to step 2.
2. **Alternative: compute `held` before `materializeConfig`.**
   - In `CheckWorker` (`worker.go:1188`), and in the agent's equivalent, evaluate `RepairAllowlist` on the stored job config before references are resolved.
   - Carry the result to `Execute` out of band, the way `ParamOverlay` is carried today. It must not be a config key that the config itself can set.
3. Whichever step is used, make a grant that is present but cannot be honoured fail closed. That means held with an empty host list, never unheld.
4. Defence in depth: in `storeCandidate` (`aichecks/repair.go`), propose rather than apply a `repair: auto` candidate that adds a `${…}` reference the previous version did not have. Keep this in addition to step 1 or 2, not instead of it.
5. Document in `web/docs/docs/features/ai-authored-checks.md` that references in an auto-repaired script do not lift the host grant.

## Tests

- `server/internal/checkworker` (worker-level, through `materializeConfig`, not by calling `Execute` directly). The fixture is a JS check with secrets and a valid repair grant whose script contains `${var:uuid}` in a comment. A run that dials a host outside `repair_hosts` must produce the `aiRepairHostRefused` error result. It fails before the fix, because the run is unheld and the dial succeeds against a test listener.
- The same case with the reference in `env` (a `${param:…}` that resolves) instead of the script.
- Agent path: the same assertion through the ws tunnel resolution (`checkworker/backend/ws_tunnel.go`), if the agent executes JS checks with grants.
- **Positive control:** the same check without any reference, dialing a host inside `repair_hosts`, succeeds. A check with a reference but no grant (a human-written script) is not held.
- `checkjs/config`: a grant present but not matching the stored script (simulating a write path that skipped `NormalizeConfig`) is treated as held with an empty list (step 3).
- `aichecks`: a `repair: auto` candidate that adds a `${var:…}` reference is proposed, not applied (step 4). A candidate whose previous version already had the same reference is still applied.

## To verify

- Every write path that can change a JS check's `script` or `env` runs `NormalizeConfig`: REST PATCH and PUT, MCP update tools, YAML/apply import (`handlers/checks/apply.go`), check clone, proposal approval and version restore. If one does not, step 1 alone would let a human edit keep a stale grant. In that case use step 2.
- Whether private agents run JS checks with a repair grant, and which binary decides `held` there.
- Whether `secretref.ContainsInConfig` short-circuits only on configs with no reference at all (`worker.go:1020`). That is the case here, so the resolution path is taken.
