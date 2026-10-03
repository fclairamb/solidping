# AI provider for AI-authored js checks

Spec 2026-10-03-07. Public docs: `web/docs/docs/features/ai-authored-checks.md`.

## k8xp deployment (BytePlus ModelArk)

| Env | Value |
|---|---|
| `SP_AI_PROVIDER` | `openai` |
| `SP_AI_BASE_URL` | `https://ark.ap-southeast.bytepluses.com/api/v3` |
| `SP_AI_MODEL` | `glm-5-3-flash-260828` (the dotted `glm-5.3-flash` answers `InvalidEndpointOrModel.NotFound`) |
| `SP_AI_API_KEY` | from gopass `solidping/byteplus/api-key`, mounted as a k8s secret |

Never write the key value anywhere else.

## Before the first deployment

- Activate the model in the Ark console. Until then chat calls answer
  `ModelNotOpen` even though `GET /api/v3/models` lists it.
- Measure GLM-5.3-flash tool calling on ~20 real prompts before trusting the
  default turn cap (`SP_AI_MAX_TURNS=12`).
- The `openai` driver ignores unknown response fields, so a
  `reasoning_content` next to `content` is harmless.

## Where things live

- Provider layer and agent loop: `server/internal/ai`.
- Generation, repair gates, guards: `server/internal/aichecks`.
- The `browser_snapshot` tool runs a small js script through the regular js
  checker: `browser.open()`, `page.goto()`, then a DOM walk in `page.evaluate`.
  It needs a browser on the server node, like any `js` check that uses
  `browser`.
- Repairs run in the `ai_repair` job, queued from the result path
  (`handlers/incidents/ai_repair.go`) on a drift-class failure.
