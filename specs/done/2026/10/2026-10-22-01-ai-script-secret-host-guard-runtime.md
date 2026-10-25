---
model: opus
effort: high
---

# AI repair and generation hand real secrets to scripts whose hosts are only checked by regex

## Problem

Found by the security scan of 2026-10-22 (finding P1).

Severity: medium. No SolidPing account is needed. Anyone who can put text in a page or response that the AI loop reads can trigger it: the operator of a third-party page the check visits, or a user posting content (comments, reviews, search results) on the monitored site. It also needs an org with an AI-authored JS check that holds secrets, and an LLM that follows the injected instructions.

Both AI loops decide whether a candidate script may run with the check's **real** secrets using a static scan of the script text:

- **Repair.** `aichecks/repair.go:378-388`: the `run_script` tool's `prepare` calls `CheckHosts(previous, script, env)` and, when it passes, returns `target.secrets`. The comment there and the one at `guards.go:150-151` both claim this check stops a prompt injection in page content from sending `secrets` to a new host. The final verification run (`repair.go:425-427`) uses the same static `CheckCandidate`, and `repair: auto` then applies the script (`repair.go:444-451`), so every later scheduled run carries the secrets too.
- **Generation.** `aichecks/service.go:504-513`: `hostsDeclared` (`guards.go:272-287`) gives real secrets to any script whose hosts all appear in the user's prompt. The comment says "page content cannot talk the model into sending them elsewhere".

Both rely on `ScriptHosts` (`guards.go:36-78`). It only sees:
- complete `scheme://host` literals inside one string (`urlLiteralRE`, `guards.go:26`);
- literal hosts passed to `tcp/udp/rdp/vnc.connect("...")` (`hostArgRE`);
- env values read through `env.NAME` or `env["NAME"]` (`envRefRE`).

Any URL the script builds at runtime is invisible to it:
- string concatenation, `Array.join`, template literals or `String.fromCharCode`;
- a host read from a fetched response;
- env values reached through dynamic keys (`env[k]`, `Object.values(env)`).

A script with no detectable host passes `CheckHosts` and `hostsDeclared` by definition. `Runner.Run` (`aichecks/runner.go:55-86`) then executes it with the secrets. At runtime only the egress guard applies, and it blocks private addresses, not "hosts the previous version did not contact".

Attack path (repair, no human in the loop):
1. The attacker changes content the check depends on, so the run fails with a drift-class error. `handlers/incidents/ai_repair.go:29-60` then queues `ai_repair` automatically. The default mode is `propose` (`checkers/checkjs/config/ai.go:59-69`), and the loop still runs in that mode.
2. The repair loop reads the page through `fetch_page` / `browser_snapshot` (`repair.go:391`) or through the failing run's output. The page holds instructions telling the model to send `secrets.<NAME>` to a host it assembles at runtime. The model knows the secret names (`repair.go:750-751`).
3. `run_script` executes that candidate with the real values, and the secret leaves on that test run. In `propose` mode nothing is applied afterwards, but the secret has already been sent.

The values are scrubbed from what the model sees (`scrubResult`). It does not stop the script from sending them.

> Correction (spec 2026-10-23-02): scrubbing replaces exact occurrences only, so it does not stop the model from echoing a value either: a script can return it encoded (`base64.encode`, reversed, sliced). Since that spec, a loop that ran with real values holds every later `fetch_page`, `browser_snapshot` and blank-secrets `run_script` to the same allowlist, and a refused host built at runtime is withheld from the error.

Out of scope: the earlier rejection (2026-10-16) of "a member with write access re-points a check's secrets". That remains the trust model. This spec is about a **third party** steering the AI through content, the threat both guards claim to cover.

## Proposal

1. Enforce the host allowlist at runtime, not in the script text. When a run gets real secrets, give it an allowed host set:
   - repair: `ScriptHosts(previous, env)` plus the hosts of the check's stored config;
   - generation: the hosts the user named.
   Every outbound dial and HTTP request of that run (http, websocket, tcp/udp, browser, redirects included) must target a host in the set, or the run fails with an explicit "host not allowed while secrets are loaded" error. A natural place is next to `egress.WithGuard` in `Runner.Run` (`aichecks/runner.go:67-71`). For example, a context value that the checkjs dialers and the HTTP transport consult, or an `egress.Guard` option.
2. Keep `ScriptHosts` only as an early, cheap refusal with a clear message. It must no longer be the security boundary. Update the comments at `guards.go:147-151`, `repair.go:381-382` and `service.go:505-507` to say so.
3. Treat a run where the allowlist refuses a host like `CheckHosts` failing today. The candidate is refused, and the refusal is in the attempt event.
4. Apply the same runtime allowlist to the verification run in `storeCandidate` (`repair.go:425-433`).
5. Decide what the scheduled runs of an auto-applied repair do. Recommended: keep the allowlist only for AI-loop runs, and refuse `repair: auto` for a candidate whose static host set is empty while the previous script's was not.
   - Superseded by spec `2026-10-24-01` (finding R1): that rule only caught a candidate showing no host at all. Now every run with real secrets of a version `repair: auto` applied is held to the hosts of the version before it (`ai.repair_hosts`, bound by `ai.repair_grant`), a refusal sets the check back to `repair: propose`, and `repair: auto` proposes any candidate with a network call whose host its text does not show (`hiddenHost`).

## Tests

- `aichecks/repair_test.go` (or a new `runner_allowlist_test.go`): a candidate that builds its target URL by concatenation and sends `secrets.TOKEN` to an `httptest` server that is not among the previous script's hosts.
  - Assert the server never receives the secret value and the candidate is refused.
  - Today this test fails: `CheckHosts` passes and the server receives the value.
- Same file, generation path: `hostsDeclared` with a prompt naming `acme.com` and a script that assembles `evilco.test` at runtime. Assert the evilco server sees no secret.
- Same file, dynamic env key: a script that loops over `Object.values(env)` to reach a host held only in an env value it never names. The request carrying a secret must be refused.
- Positive control: a candidate that contacts only the previous script's host, including through a URL built at runtime to that same host, still runs with the real secret, and the test server receives it.
- Positive control: a script with no secrets still reaches an arbitrary public host. The allowlist only applies when real secrets are loaded.
- `aichecks/guards_test.go`: keep the existing `ScriptHosts` cases. They pin the early refusal, not the boundary.

## To verify

- Which checkjs globals open connections (http, fetch-like helpers, websocket, tcp/udp/sip/rdp/vnc, browser), and whether they all go through one dial seam the allowlist can hook (`checkers/checkjs/httpsession.go`, `socket.go`, `websocket.go`, `browser.go`).
- Whether the headless browser used by the browser API can be held to a host allowlist (request interception), or whether browser use should simply run with blank secrets while the allowlist is active.
