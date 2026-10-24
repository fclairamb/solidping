---
model: opus
effort: high
---

# `repair: auto` applies a candidate that keeps a readable host and adds one built at runtime

## Problem

Found by the security scan of 2026-10-24 (finding R1).

Severity: medium.
- **Who can trigger it:** the same as P1 (spec `specs/done/2026/10/2026-10-22-01-ai-script-secret-host-guard-runtime.md`). A third party who controls content the repair loop reads, with no SolidPing account: a third-party page in a multi-step check, or user content on the monitored site.
- **What the target org needs:** an AI-authored JS check that holds secrets and has `ai.repair: auto`. The model has to follow the injected instructions.
- **Unlike P1/Q1, the secret leaks on every scheduled run**, not once inside the loop. It does not need the model to see a secret value (Q1): the candidate script reads `secrets.<NAME>` itself.

The runtime host allowlist from P1 (`egress.WithHostAllowlist`) is used only inside the AI runner (`aichecks/runner.go:99`). A scheduled run of the check has the egress guard only, which blocks private addresses and lets any public host through. P1 step 5 knew this and kept `repair: auto` from applying a candidate whose connections are built at runtime. The implementation catches only one shape of that:

- `hostsHidden` (`aichecks/guards.go:348-350`) is true only when the candidate's text shows **no** host at all while the previous version's did.
- `storeCandidate` (`aichecks/repair.go:451-458`) proposes instead of applying only in that case. Its comment says the rule is about "a candidate whose hosts the text no longer shows (built at runtime)".
- `CheckCandidate` (`guards.go:260-274`) and `CheckHosts` (`guards.go:208-218`) read literal hosts only (`ScriptHosts`, `guards.go:40-81`). A host assembled from parts, decoded from base64 or taken from a computed key is invisible to them. Those comments say so too.
- The verification run (`repair.go:434`) is held to `repairHosts`, but it only refuses connections the run actually makes.

So a candidate that does both of these passes every gate and is applied by `applyRepair` (`repair.go:460-467`, `:618-640`):
1. it keeps the previous version's literal host, so `hostsHidden` is false;
2. it adds a request to a host built at runtime that carries `secrets.<NAME>`, behind a condition that does not hold during the loop or the verification run (a date, a counter, a random draw).

**Scenario:**
1. The attacker makes the check fail with a drift-class error, so `ai_repair` is queued (`handlers/incidents/ai_repair.go`).
2. The injected page text tells the model to write such a candidate.
3. The loop run and the verification run pass: the extra branch never runs, so the allowlist refuses nothing.
4. `repair: auto` applies the candidate (within the 24 h cooldown). Once the condition holds, scheduled runs send the secret to the attacker's public host. Nothing records a refusal.

`propose` mode (the default) stores the same candidate as a proposal. A human who approves it without spotting the extra branch gets the same result. That is the review trust model, not this finding. The generation flow is the same case: the user saves the returned script themselves.

## Proposal

The goal: a version written by `repair: auto` can never reach, on schedule, a host the version before it could not.

1. **Recommended: enforce the allowlist on scheduled runs of an auto-applied version.**
   - When `applyRepair` writes a version, record its allowed hosts (`repairHosts(previous, env)`) with the version, e.g. in the check's `ai` block (`checkers/checkjs/config/ai.go`) or the version row.
   - When a JS check whose current version came from `CheckVersionOriginAIRepair` runs with real secrets, hold it to those hosts through `egress.WithHostAllowlist`, the same seam `Runner.run` uses (`aichecks/runner.go:96-100`).
   - A refusal fails the run with a clear error, not silently.
   - A human edit of the script (any non-AI origin) clears the stored allowlist.
   - **To verify:** where checkjs runs scheduled jobs and whether it can read the version origin and the stored hosts.
2. **Make the static gate fail closed as well,** so the case is also caught before applying. In `storeCandidate`, refuse auto-apply (fall back to a proposal) whenever the candidate can reach a host its text does not show. A cheap rule: refuse when the candidate has more network call sites (`http.*`, `fetch`, `websocket`, socket `connect`) than the previous version, or a call site whose URL or host argument is not a string literal or an `env` read. Rename `hostsHidden` to match.
3. Fix the comment at `repair.go:451-453` and the P1 spec's step 5 so they describe the rule that actually applies.
4. Docs: `web/docs/docs/features/ai-authored-checks.md` says what `repair: auto` can and cannot apply.

Step 2 alone stays a heuristic over script text, which is the trap P1 already fell into. Step 1 is the boundary.

## Tests

- `aichecks/service_test.go` regression, next to `TestRepairAutoAppliesAVerifiedCandidate` (`:448`):
  - The previous script contacts `acme.test`. The candidate keeps that literal and adds a branch to a host assembled at runtime, guarded by a condition the test controls.
  - With `repair: auto`, assert the candidate is **not** applied: the outcome is a proposal, and the reason names the hidden host.
  - This test fails on current `main`.
- Scheduled-run test for step 1, in the checkjs package:
  - An auto-applied version with stored hosts `[acme.test]` runs with real secrets, and the branch to an `evilco.test` test server fires.
  - Assert the run fails with the host-refusal error and the `evilco.test` server receives nothing.
- Positive controls:
  - A candidate that only changes a selector or an assertion on the same literal host is still auto-applied (existing test stays green).
  - An auto-applied version that calls only `acme.test`, including through a URL built at runtime to that same host, keeps running and the test server gets the secret.
  - After a human edit of the script, the stored allowlist is gone and a newly added literal host is reachable.

## Open questions

- Should a scheduled run that hits the stored allowlist also flip the check back to `repair: propose`, so the next repair needs a human? Recommended: yes, and emit an event, since a refusal on a scheduled run means the applied version is doing something its own loop never showed.
