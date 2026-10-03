---
sidebar_position: 27
title: AI-authored checks
---

# AI-authored checks

Describe what you want to watch in plain words. An AI writes a
[JavaScript check](/features/javascript-checks) script for it, tests it, and
you save it. From then on the plain script runs on the workers like any other
`js` check, with zero AI calls.

When the target changes its pages and the script breaks, SolidPing can propose
a repair. It never repairs a real outage.

The feature is off unless the server has an AI provider (see
[Configuration](#configuration)). Without one, the "Describe it" button is hidden
and the endpoints answer 404.

## Describe it

On the checks page, click **Describe it**.

1. Write what the check should watch, for example "log in to
   `https://app.acme.com` with the test account, the dashboard shows at least
   one project". Add plain parameters (`env`, e.g. `BASE_URL`) and secrets
   (`secrets`, e.g. `PASSWORD`) if the script needs them.
2. SolidPing proposes a **contract**: the ordered list of assertions the script
   will check. Edit it until it says what you want.
3. Generate. The AI explores the target (`fetch_page`, `browser_snapshot`),
   writes a script and runs it (`run_script`) until a run returns `up`, or the
   turn cap is hit. You see the script and its last run.
4. Save. The check is a normal `js` check with an `ai` block in its config.

The AI never receives a secret value. It only sees the secret names, and the
script reads them as `secrets.NAME`. A test run only gets the real values when
every host the script contacts appears in your description or parameters.
Secret values are scrubbed from everything handed back to the AI. The scrubbing matches the literal value, so a script that prints an encoded form (base64, URL-encoded) of a secret to the console would not be caught: never log secrets.

## The `ai` block

```yaml
type: js
config:
  script: |
    ...
  ai:
    prompt: "the acme api lists at least one project"
    contract:
      - "GET /api answers 200"
      - "the list has at least one project"
    model: glm-5-3-flash-260828
    generated_at: "2026-10-03T10:00:00Z"
    repair: propose
```

| Field | Meaning |
|---|---|
| `prompt` | Your description |
| `contract` | The assertions you confirmed. Required when `prompt` is set |
| `model` | The model that wrote the current script |
| `generated_at` | When it was written |
| `repair` | `off`, `propose` (default) or `auto` |

The script stays editable, exports with `sp export` and runs unchanged on cloud
workers and private agents. Workers never see the provider or the key.

Saving a generated script records the version with origin `ai_generate` in the
[check history](/features/check-history).

## Drift-only repairs

Generated scripts tag their failures: `output.failure = "assertion"` when the
service misbehaves, `output.failure = "drift"` when the script cannot find what
it expected (a selector, a field, a page structure).

A repair attempt starts only when all of these hold:

- the failure is drift: a script exception, or `output.failure == "drift"`. A
  clean `down` tagged `assertion`, a timeout or a connection error never
  qualifies;
- 3 consecutive failing runs;
- the target looks healthy: a GET on the script's base URL answers 2xx or 3xx;
- at most one attempt per check per 24 h, and at most 20 per organization per
  UTC day.

The AI gets the old script, the contract, the failure output and a fresh look
at the target. Before a candidate is kept, mechanical guards run:

- it contacts no host the previous version did not (no candidate ever runs with
  the secrets against a new host);
- it adds no `try`/`catch`;
- it can still report `down`, and uses no new protocol API;
- a verification run returns `up`.

With `repair: propose`, a candidate that passes is stored as a **proposed**
version (origin `ai_repair`). The check stays down, the incident stays open.
Approve or reject it from the check's **History** page, with the diff and a
one-line reason. A candidate failing a guard is dropped.

With `repair: auto`, a candidate that passed every guard and its verification
run is applied directly as a new version. The incident resolves on the next
run, and the author of the replaced version gets an email with the diff. Undo it
with a normal version restore. A candidate failing a guard or the verification,
or a second repair within 24 h, falls back to a proposal.

Every attempt is on the check's timeline (`check.ai_repair_attempted`,
`check.ai_repair_proposed`, `check.ai_repair_applied`).

## Bring your own agent

The [MCP server](/features/mcp) exposes `run_js_script`, `fetch_page` and
`browser_snapshot` with the same behavior, so any external agent can author
`js` checks. They need no AI provider on the server.

## Configuration

| Variable | Default | Description |
|---|---|---|
| `SP_AI_PROVIDER` | - | `openai` or `anthropic`. Empty = off |
| `SP_AI_BASE_URL` | driver default | Endpoint base URL |
| `SP_AI_API_KEY` | - | Provider key, server only |
| `SP_AI_MODEL` | - | Model ID, passed verbatim |
| `SP_AI_MAX_TURNS` | `12` | Agent loop cap |
| `SP_AI_TIMEOUT` | `120s` | Per-call timeout |

The `openai` driver speaks Chat Completions (`POST {base_url}/chat/completions`
with `tools`), which covers OpenAI, BytePlus ModelArk, Mistral, Groq, DeepSeek,
Gemini's OpenAI endpoint, OpenRouter, LiteLLM, Ollama and vLLM. The `anthropic`
driver uses the Messages API with prompt caching on the system prompt.

BytePlus ModelArk example:

```bash
SP_AI_PROVIDER=openai
SP_AI_BASE_URL=https://ark.ap-southeast.bytepluses.com/api/v3
SP_AI_MODEL=glm-5-3-flash-260828   # the dotted name glm-5.3-flash is refused
SP_AI_API_KEY=...                  # activate the model in the Ark console first
```

Every LLM call is logged with the organization, the check, the purpose
(`contract`, `generate`, `repair`) and the token usage, and recorded as a
`check.ai_usage` event. On the SaaS, the `maxAiTokensPerDay` entitlement caps
the tokens an organization spends per UTC day (429 once spent). Self-hosted is
unlimited: your key pays.
