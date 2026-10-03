---
model: opus
effort: high
---

# AI-authored js checks: turn a prompt into a `js` check script, run it with zero AI, repair only on drift

## Problem

Writing a `js` check (`server/internal/checkers/checkjs`) takes knowledge of its
API (`http`, `browser`, `socket`, `websocket`, `rdp`, `vnc`, `solidping.check()`,
`console`, `env`, `secrets`) and of the target page. Most users can describe what
they want to watch ("log in with the test account, the dashboard shows at least
one project") but won't write the script.

Running an LLM on every check execution would be slow, costly and flaky. The LLM
should write the script once, and from then on the plain script runs.

The trap: a failing check is what a monitor exists to report. If every failure
triggers a rewrite, the LLM makes the test green (drops the assertion, widens the
match, wraps it in try/catch) and the check heals over a real outage. Repair must
only target **drift** (the script broke) and never a **real incident** (the
service broke), and must be a proposal by default.

## Proposal

### 1. Provider layer (fully configurable)

New package `server/internal/ai`:

```go
type Provider interface {
    Complete(ctx context.Context, req Request) (*Response, error)
}
// Request:  System, Messages, Tools []ToolDef, MaxTokens
// Response: Text, ToolCalls []ToolCall, StopReason, Usage{InputTokens, OutputTokens}
```

Two drivers:
- `openai`: OpenAI **Chat Completions** (`POST {base_url}/chat/completions`,
  `tools` / `tool_calls`). Covers BytePlus ModelArk, OpenAI, Mistral, Groq,
  DeepSeek, Gemini's OpenAI endpoint, OpenRouter, LiteLLM, Ollama, vLLM.
- `anthropic`: native Messages API (official Go SDK), with prompt caching on the
  system prompt (the JS API reference is a large stable prefix).

Nothing provider-specific is hardcoded. Config in `server/internal/config/config.go`
(env vars registered like the other `SP_*` ones, `server/internal/config/envvars.go`):

| Env | Meaning |
|---|---|
| `SP_AI_PROVIDER` | `openai` \| `anthropic`. Empty = feature off. |
| `SP_AI_BASE_URL` | Endpoint base URL. |
| `SP_AI_API_KEY` | Secret. Never logged, never sent to workers or agents. |
| `SP_AI_MODEL` | Model ID passed verbatim. |
| `SP_AI_MAX_TURNS` | Agent loop cap (default 12). |
| `SP_AI_TIMEOUT` | Per-call timeout. |

First deployment (k8xp):
- `SP_AI_PROVIDER=openai`
- `SP_AI_BASE_URL=https://ark.ap-southeast.bytepluses.com/api/v3`
- `SP_AI_MODEL=glm-5-3-flash-260828` (BytePlus ID for `glm-5.3-flash`; the dotted
  name returns `InvalidEndpointOrModel.NotFound`)
- key from gopass `solidping/byteplus/api-key`, mounted as a k8s secret.
Document the gopass path, never the value.

The agent loop is SolidPing's own code (call, run tools, append results, repeat,
stop at `SP_AI_MAX_TURNS`). No agent framework. Every call is logged with org,
check, purpose (`generate` / `repair`) and token usage.

### 2. Check storage: a `js` check with an `ai` block

`server/internal/checkers/checkjs/config/config.go`, `JSConfig` gets an optional
`AI *AIConfig` (snake_case keys like the DNS fields):

| Field | Meaning |
|---|---|
| `prompt` | The user's description. |
| `contract` | Ordered list of human-readable assertions, confirmed by the user. |
| `model` | Model that wrote the current script. |
| `generated_at` | Timestamp. |
| `repair` | `off` \| `propose` (default) \| `auto`. `auto` ships in v1: a candidate that passes every guard and whose verification run returns `up` is applied directly as a new `applied` version (origin `ai_repair`, `base_version` set), the incident resolves on the next run, and the owner is notified with the diff. The user can undo it with a normal version restore. A candidate failing any guard or verification falls back to a `proposed` version, as in `propose`. |

- Parse in `FromMap`, emit in `GetConfig`, validate in `ValidateSpec`
  (contract non-empty when `prompt` set, `repair` in the closed set `off|propose|auto`).
- The script stays a normal `js` script: editable, exported by `sp export`,
  executed unchanged by cloud workers and private agents. Workers never see the
  AI config's provider or key (they only exist on the server).
- History and repair proposals use check versions (spec 2026-10-03-06, which
  must land first). A generated script is saved as a version with origin
  `ai_generate`, a repair as a `proposed` version with origin `ai_repair` and
  `base_version` set.

### 2b. Migrations

None in this spec. The `check_versions` table comes from spec 2026-10-03-06.
Also no migration for:
- The `ai` block: `checks.config` is already JSON (`models/check.go:227`).
- AI usage and repair attempts: new event types on `events`
  (`models/event.go`, `event_type` is free text) with tokens in `payload`. The
  per-check rate limit and the org daily cap query them.
- The SaaS token budget: a new key in `org_entitlements.payload` (JSON,
  `models/org_entitlements.go:53`).

### 3. Generation (server-side agent loop)

Endpoint `POST /api/v1/orgs/$org/checks/ai/generate` (and a step to confirm the
contract before writing the script):
1. LLM turns `prompt` into `contract`. Returned to the user for confirmation.
2. With the confirmed contract, the loop runs with tools:
   - `fetch_page(url)`: status, headers, truncated body.
   - `browser_snapshot(url)`: accessibility tree via the existing browser runtime
     (`checkjs/browser.go`).
   - `run_script(script)`: executes through `JSChecker.Execute`
     (`checkjs/checker.go:120`) with the check's env/secrets **names** only; returns
     status, output, console (capped at `maxConsoleOutput`, `checker.go:69`).
3. System prompt: the js API reference (docs + `checkjs/samples.go`) and the rule
   that the script must tag failures `output.failure = "assertion" | "drift"`.
4. Loop ends when `run_script` returns `up` or the turn cap is hit. The user sees
   the script and the last run before saving.

The LLM never receives secret values. Generated scripts reference `secrets.X`.

### 4. Steady state

Plain `js` check execution. Zero AI calls.

### 5. Drift-only repair proposals

A repair attempt starts only when **all** hold:
- failure classified as drift: script exception, or `output.failure == "drift"`.
  A clean `down` with `failure == "assertion"`, a timeout or a connection error
  never qualifies;
- N consecutive failures (default 3);
- target looks healthy: GET on the script's base URL returns 2xx/3xx;
- rate limit: one attempt per check per 24 h, plus an org-level daily cap.

The repair loop gets the old script, contract, failure output and fresh
snapshots. Before a candidate is accepted, mechanical guards:
- hosts the script contacts are a subset of the previous version's (blocks
  exfiltration of `secrets` via prompt injection in page content);
- no new `try`/`catch` wrapping a contract assertion;
- `run_script` returns `up`.

Accepted candidates are stored as a `proposed` check version (spec
2026-10-03-06, `base_version` = the current version). The check stays down
and the incident stays open. The user gets the diff plus a one-line reason in the
dashboard and approves or rejects.

With `repair: auto`, a candidate that passed the guards and a verification run
returning `up` is stored directly as an `applied` version (origin `ai_repair`,
`base_version` = the current version) instead of `proposed`, and a notification
with the diff is sent. Auto-repair is rate-limited (max 1 applied repair per check
per 24 h, tracked via version origin and timestamp); beyond that it falls back to
`proposed`.

### 6. MCP tools (bring-your-own agent)

On the existing MCP server (`server/internal/mcp`, tool names in
`constants.go:92` area, check tools in `tools_checks.go`), add `run_js_script`,
`fetch_page`, `browser_snapshot` with the same semantics as section 3, so any
external agent can author `js` checks without SolidPing calling an LLM. Same auth
scope rules as `validate_check` (`scope.go`).

### 7. Dashboard

- Check creation: "Describe it" flow (prompt → confirm contract → watch
  generation → save). Dedicated route, not a modal.
- Check detail: prompt and contract. History, diff and approve/reject of repair
  proposals come from the History tab of spec 2026-10-03-06.
- Hidden when `SP_AI_PROVIDER` is empty.

### 8. Docs

`web/docs/`: AI-authored checks page, `SP_AI_*` in the configuration reference,
BytePlus as an example of the `openai` driver. CHANGELOG entry.

## Tests

- `server/internal/ai`: `openai` driver against an `httptest` server: text reply,
  tool_calls reply, 4xx error mapped, key never in logs. Same for `anthropic`.
- Agent loop: fake provider that calls `run_script` twice then stops; turn cap
  hit returns an error, not a saved script.
- `checkjs/config`: `ai` block round-trip `FromMap`/`GetConfig`; invalid `repair`
  value and empty contract rejected.
- Repair trigger: table test over (exception, drift tag, assertion tag, timeout,
  connection refused, consecutive count, target health, rate limit). Only drift
  rows with all gates open start a repair.
- Repair guards: candidate adding a new host is rejected; candidate wrapping an
  assertion in try/catch is rejected; valid candidate becomes a pending proposal
  and the check status is unchanged.
- A valid repair candidate is saved as a `proposed` version with origin
  `ai_repair` and `base_version` set; the check's config is unchanged until
  approval.
- `repair: auto`: a valid candidate verified `up` becomes an `applied` version with
  origin `ai_repair` and the check config is updated; a guard failure, failed
  verification, or second repair within 24 h falls back to a `proposed` version.
  Restoring the previous version undoes it.
- MCP: `run_js_script` returns the checker result; scope test like
  `scope_test.go`; unauthorised token refused.
- Feature off: with `SP_AI_PROVIDER` empty, generate endpoint answers 404 and the
  MCP tools that need an LLM are not listed (the three tools in section 6 don't
  need one and stay listed).
- Playwright (`web/dash0/e2e/`): generation flow with a stubbed backend, approve
  a repair proposal.

## To verify

- BytePlus account activation: the API key reaches ModelArk (`GET /api/v3/models`
  lists `glm-5-3-flash-260828`), but chat calls return `ModelNotOpen` until the
  model is activated in the Ark Console. Activate before testing the first
  deployment.
- GLM-5.3-flash tool-calling quality on a multi-turn loop: measure on ~20 real
  prompts before defaulting the turn cap.
- Whether BytePlus returns `reasoning_content` alongside `content`; the `openai`
  driver should ignore unknown fields.
- How the browser runtime can produce an accessibility snapshot outside a check
  run (`checkjs/browser.go`, `checkers/checkbrowser/session.go`).

## Resolved open questions

- Ship `auto` repair mode in v1: yes. Implement `repair: auto` as described in the
  config table and section 5 (guards + verification run required, rate limit of 1
  applied repair per check per 24 h, falls back to `proposed` otherwise).
- SaaS: use a server key with an entitlement-backed token budget in v1. Bring-your-own
  key is out of scope for v1.
