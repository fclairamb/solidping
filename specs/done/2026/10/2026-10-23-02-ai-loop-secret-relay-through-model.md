---
model: opus
effort: high
---

# AI loops relay an encoded secret through the model to fetch_page or a secret-less run

## Problem

Found by the security scan of 2026-10-23 (finding Q1).

Severity: medium. This is a bypass of the P1 fix (`c09fdd1c`, spec `specs/done/2026/10/2026-10-22-01-ai-script-secret-host-guard-runtime.md`), with the same prerequisites:
- No SolidPing account is needed.
- Anyone who can put text in content the AI loop reads can trigger it: the operator of a third-party page the check visits, or a user posting content on the monitored site.
- The target org needs an AI-authored JS check holding secrets, and the model has to follow the injected instructions.

The P1 fix holds the script's own connections to a runtime host allowlist (`aichecks/runner.go:80-85`, `egress/allowlist.go`). It still relies on one assumption that the done spec states openly: `scrubResult` "stops the model from echoing" the values. That assumption does not hold.
- `scrubResult` / `secretReplacer` (`aichecks/runner.go:199-246`) replace **exact** occurrences of each secret value only.
- A script can return a transformed value: `base64.encode(...)` is a built-in global (`checkers/checkjs/checker.go:525-549`), and so are string reversal, splitting with separators, char codes or a slice of the value. That output contains no network access, so the allowlist never fires.
- `runScript` returns the whole result to the model (`aichecks/tools.go:245`, `forModel(payload)`), so the encoded value reaches the model's context.
- The same loops give the model tools that reach any public host with no secrets loaded, so no allowlist applies:
  - `fetch_page` and `browser_snapshot` (`aichecks/runner.go:129-137`, `tools.go:54-99`), in repair (`aichecks/repair.go:394`) and generation (`aichecks/service.go:520-524`);
  - in generation, `run_script` itself: a script naming an undeclared host runs with blank secrets and no allowlist (`aichecks/service.go:513-516`), so it can carry a value the model copies into it.
- `HostNotAllowedError` targets are scrubbed the same exact-match way (`runner.go:92-95`), so a refused hostname that embeds an encoded value is another way back to the model.

Attack path (repair, no human involved; generation is the same with a member's prompt as the trigger):
1. The attacker changes content the check depends on, so the run fails with a drift-class error. `handlers/incidents/ai_repair.go` queues `ai_repair`. The default mode `propose` still runs the loop.
2. Text in the page read through `fetch_page` / `browser_snapshot` tells the model to run a candidate that returns an encoded form of `secrets.<NAME>`. The names are in the brief. The candidate contacts only allowed hosts, or none, so it runs with the real values.
3. The model reads the encoded value in the tool result and calls `fetch_page` on an evilco.example URL carrying it. The secret has left, with nothing applied to the check.

Impact: the check's real credentials (API tokens, passwords) go to a third party.

## Proposal

1. Make the boundary independent of what the model sees. Recommended: once a loop has made a run with real secret values, refuse every later outbound tool call of that loop that is not held to the same allowlist:
   - `fetch_page` and `browser_snapshot` either go through the run's `HostAllowlist` or are refused with a clear message;
   - in generation, a blank-secrets `run_script` after a real-secrets run gets the same allowlist.
   The flag lives on the loop (`runRecorder` or the loop state in `repair.go:374-394` / `service.go:500-524`), so it covers both paths.
2. Alternative if (1) hurts repair too much: never return run output to the model for a run that had real values. Return only status, duration and the error class, and keep the full output for the user's attempt view.
3. Keep exact-match scrubbing as defence in depth. Update the comments at `runner.go:53-62` and the done spec's claim that scrubbing stops echoes.
4. Also scrub the refused target the same way the fix chooses for output (`runner.go:92-95`).

## Tests

- `aichecks/repair_test.go` (or `runner_allowlist_test.go`), regression: a fake model whose first call is `run_script` returning `base64.encode(secrets.TOKEN)` and whose second call is `fetch_page` on an `httptest` server outside the allowlist, carrying the value from the first result.
  - Assert the server never receives the encoded value, and the refusal is recorded on the attempt.
  - Today this test fails: `fetch_page` sends it.
- Same file, generation path: a real-secrets run followed by a blank-secrets `run_script` to an undeclared `httptest` host carrying the value. Assert the host sees nothing.
- Same file: a refused host such as `<encoded>.evilco.test`. Assert the error text the model gets does not contain the encoded value (only if Proposal 2 or 4 covers it).
- Positive control: a loop that never ran with real values can still `fetch_page` an arbitrary public `httptest` URL.
- Positive control: a repair whose candidate only reaches the previous script's host still runs with the real secret and can still pass.

## To verify

- Whether the AI loop reaches any other model-driven outbound tool than `fetch_page`, `browser_snapshot` and `run_script` (MCP authoring tools in `mcp/tools_jsauthoring.go` running the same loop).
- Whether the progress events (`ProgressToolResult`, `runSummary`) and attempt events persist unscrubbed encoded values. They stay in the org, but the fix should not widen what they show.
