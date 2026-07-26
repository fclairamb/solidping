---
model: sonnet
effort: medium
---

# Heartbeat checks: token must be viewable and regenerable from the check page

## Problem

On heartbeat checks (e.g. `/dash0/orgs/$org/checks/$checkUid`), users currently
cannot view the heartbeat token / ping URL (previous behavior), and there is no
way to regenerate it at all.

What's actually going on:

- The token is generated once at check creation
  (`server/internal/checkers/checkheartbeat/checker.go:34-41`, `generateToken`
  at `:60-68`) and stored in the public `config["token"]` JSONB — it is
  deliberately **not** a secret (`server/internal/checkers/checkheartbeat/secret_fields.go:19-21`,
  rationale comment at `:3-18`).
- The detail page's `HeartbeatEndpoint` panel
  (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:303-348`) only
  renders when `check.config?.token` is truthy (`:1173-1174`). A batch-2026-07-16
  secrets-leak fix briefly classified the token as secret, which redacted it
  from GET responses and silently hid the panel — that's the "unable to view"
  regression the user sees on the deployed env. Commit `994bc851` already
  reverted the classification on HEAD, and `TestHeartbeatTokenStaysPublic`
  (`server/internal/handlers/checks/plaintext_secrets_test.go:182-215`) guards
  it.
- There is **no regenerate endpoint**: check routes
  (`server/internal/app/server.go:691-772`) have no token-rotate route, the
  edit form intentionally sends `{}` config for passive checks
  (`web/dash0/src/components/shared/check-form.tsx:674-675`), and the heartbeat
  form module has no token field
  (`web/dash0/src/components/checks/form/types/misc.tsx:761-800`). Once minted,
  a token can never be changed — bad if it leaks (it's embedded in ping URLs,
  cron lines, CI configs…).

## Proposal

Make the token both viewable (verify/keep) and regenerable (new):

1. **Backend — rotate endpoint.** Add
   `POST /api/v1/orgs/:org/checks/:checkUid/rotate-token`, mirroring the
   webhook signing-secret rotation pattern:
   - Route next to the other check routes (`server/internal/app/server.go:691-772`);
     model on `POST /:uid/rotate-secret` (`server/internal/app/server.go:1084`).
   - Handler + service modeled on `RotateWebhookSecret`
     (`server/internal/handlers/integrations/handler.go:242-255`,
     `server/internal/handlers/integrations/service.go:1068-1113`): generate a
     fresh token via the existing `generateToken()` logic, rewrite
     `check.Config["token"]`, persist, return the updated check response.
   - 400/404 for non-heartbeat checks / unknown checks; standard auth (org
     member with check-edit permission, consistent with other check mutations).
   - No grace period needed unless trivial to add — heartbeat pings are
     frequent, and the user immediately updates the sender. (Webhook rotation
     keeps the old secret 24 h; decide and document either way.)
   - Update `server/internal/app/openapi/openapi.yaml` for the new route.
2. **Frontend — view + regenerate in the `HeartbeatEndpoint` panel**
   (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:303-348`):
   - Keep showing the ping URL + curl command with copy buttons (existing).
   - Add a "Regenerate" button wired to a new `useRotateHeartbeatToken` hook in
     `web/dash0/src/api/hooks.ts` (model: `useRotateWebhookSecret` at
     `:3319-3334` — POST + query invalidation).
   - Regeneration invalidates every existing ping URL, so confirm before
     rotating (destructive-style confirmation; the action itself is a rotate,
     not a delete — don't use the trash icon).
   - Follow the design reference (`web/dash0/src/routes/orgs/$org/design-reference.tsx`)
     for the button/confirm primitives; the `WebhookSigningPanel`
     (`web/dash0/src/components/integrations/integration-form.tsx:966-988`) is
     the UX precedent.
3. **Tests.**
   - Backend: rotate changes the token, old token gets 401 on
     `/api/v1/heartbeat/:org/:identifier`
     (`server/internal/handlers/heartbeat/service.go:144-152`), new token gets
     200; non-heartbeat check → 400; keep `TestHeartbeatTokenStaysPublic`
     green so the view regression can't silently return.
   - Frontend: Playwright — heartbeat check detail shows the ping URL, and the
     regenerate flow updates the displayed URL.

Out of scope: making the token a secret again (explicitly rejected in
`secret_fields.go`), multi-token support.
