---
model: opus
effort: medium
---

# The `repair: auto` host hold skips runs whose secrets come from `${param:}` / `${env:}` references instead of `secrets`

## Problem

Found by the security scan of 2026-10-26 (finding U1).

- **Severity:** low. It is a variant of S1 (`specs/done/2026/10/2026-10-25-01-ai-repair-grant-dropped-by-reference-resolution.md`) and narrows the R1 fix (`specs/done/2026/10/2026-10-24-01-ai-repair-auto-apply-partial-runtime-host.md`).
- **Who can trigger it:** the same as R1 and S1. A third party who controls content the repair loop reads, with no SolidPing account. Two conditions on the target org:
  - it has an AI-authored JS check with `ai.repair: auto`;
  - that check takes its credential from a `${param:…}` (or allowed `${env:…}`) reference in `env` or in the script, and holds no `secrets` value.
- The model must also follow the injection, and the candidate must get past the static `hiddenHost` gate, as in S1.

**The flaw.** A scheduled run is held to its repair grant only when the `secrets` map has a value:

- `JSChecker.Execute` takes the hold decided on the stored config (`checkers/checkjs/checker.go:159-162`). It then installs the allowlist only if `held && hasSecretValues(cfg.Secrets)` (`checker.go:165`).
- `hasSecretValues` looks at `cfg.Secrets` alone (`checker.go:257-266`).
- Before `Execute`, `materializeConfig` merges the `${param:…}` overlay and resolves the `${env:…}` / `${var:…}` references in every config string. That covers the strings nested in `env` (`checkworker/worker.go:1019-1063`, overlay at `:1050`; `secretref/secretref.go:198-205`).
- `LookupParam` returns the plaintext of a secret-flagged parameter (`secretref/resolvers.go:59-61`). The docs allow references in both maps (`web/docs/docs/features/javascript-checks.md:92`).
- So a check with `env: { TOKEN: "${param:api-token}" }` and no `secrets` runs on schedule with the real token in `env.TOKEN`, and with no host allowlist.

**Why the repair loop does not catch it.** The loop and the verification run use the stored `env`, where the reference is still the literal `${param:api-token}`. They pass `target.secrets` (`aichecks/repair.go:391`, `:435`), and the runner holds a run only when those secrets have a value (`aichecks/runner.go:72`, `:97`). In the loop the candidate therefore sees nothing secret and reaches its hosts unheld. That is harmless there, but `repair: auto` then applies the candidate with a grant:

- `addedReference` does not fire, because the reference sits in the previous `env`, not in the candidate script.
- `hiddenHost` is a regex over direct call syntax (`aichecks/guards.go:352-388`). A call through an aliased API is not counted (S1).

On schedule the grant is present and `held` is true, but the run is not held, because `Secrets` is empty. Nothing produces `aiRepairHostRefused`, and `RevokeAutoRepair` never fires.

**Impact.** The injected branch can send the resolved parameter value to a host built at runtime. The docs promise the opposite: "Every run holding real secrets is held to them" (`web/docs/docs/features/ai-authored-checks.md:150-151`). The current test pins the gap as intended: `TestRepairGrantOnlyHoldsItsScriptWithSecrets` ("no secrets" case, `checkers/checkjs/repair_allowlist_test.go:134-160`).

## Proposal

1. Hold every scheduled run of a granted config, whatever its secrets (`checkers/checkjs/checker.go:165`):
   - Drop `hasSecretValues(cfg.Secrets)` from the condition when the hold comes from a stored grant (`held` from `WithStoredRepairHold` or `RepairAllowlist`).
   - The grant lists the hosts the previous version reached, so a passing version loses nothing.
   - Keep the `egress.HostAllowlistFrom(ctx) == nil` clause: the AI loop keeps its own list.
2. If step 1 is judged too broad, decide "holds real secrets" on what the run actually carries:
   - a non-empty `checkJob.ParamOverlay`;
   - or any `${param:}` / `${env:}` reference in the stored `script` or `env`.
   - Carry the result out of band next to the hold, the way `WithStoredRepairHold` carries `held` (`checkworker/worker.go:1191-1197`, `:1370`). It must not be a config key.
3. Update `web/docs/docs/features/ai-authored-checks.md:148-160` to match: a version `repair: auto` applied is held on every scheduled run, or on every run holding a secret, references included.

## Tests

- `checkers/checkjs/repair_allowlist_test.go`: turn the "no secrets" case of `TestRepairGrantOnlyHoldsItsScriptWithSecrets` around.
  - A granted config with an empty `Secrets` and an `env` value standing for a resolved parameter runs a branch to an `evilco` sink.
  - The run is refused (`aiRepairHostRefused`), and the sink gets no hit.
  - This fails before the fix, since today the sink is hit.
  - Keep the "no grant" case as the positive control: a human-written script with the same branch still reaches the sink.
- `checkworker/repair_grant_test.go`: go through the real claim, `materializeConfig` and the JS checker.
  - The stored config has a grant, no `secrets`, and `env: { TOKEN: "${param:token}" }` resolved through a real parameter. A run whose branch targets a runtime-built host is refused.
  - Positive control: the same check reaching only its granted host is up.
- Agent path (`checkworker/repair_grant_agent_test.go`): the same no-`secrets` case is held.

## To verify

- Whether any non-repair caller runs `JSChecker.Execute` on a granted config without secrets and relies on it being unheld (`grep -rn 'JSChecker{}' server/internal`). The AI runner installs its own list first, so step 1 should not affect it.
