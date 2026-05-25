# Standard Webhooks: Signed, Typed Webhook Delivery

## Context

Webhook channels currently send unsigned HTTP POST requests with a flat, ad-hoc payload. Recipients
have no way to verify that a request genuinely came from SolidPing, making the integration
unusable in security-conscious environments. There is also no standard for the payload shape or
event type naming.

We adopt the [Standard Webhooks](https://www.standardwebhooks.com/) specification: a vendor-neutral
open standard (also used by Svix, Clerk, Resend, Lob, …) for signed, replay-protected, idempotent
webhook delivery. Adopting it means users can reach for any Standard-Webhooks receiver library
rather than writing custom verification code.

The old payload format is not preserved.

## Goal

- Every outbound webhook is signed with HMAC-SHA256 and carries the three Standard Webhooks headers.
- Each webhook channel has its own per-channel signing secret, stored AES-encrypted.
- Secrets are rotatable with a grace period (two active secrets simultaneously during transition).
- The payload is reworked into a clean `{ type, timestamp, data }` envelope.
- Users can test a webhook channel directly from the UI.
- No new Go dependencies — everything uses stdlib (`crypto/hmac`, `crypto/sha256`,
  `encoding/base64`, `crypto/rand`).

## Non-goals

- Webhook delivery logs / retry history UI (separate spec).
- Per-event-type subscription filtering (all events still go to all attached webhook channels).
- Outbound mTLS or IP allowlisting.
- Svix or any third-party delivery infrastructure.

## Standard Webhooks signing mechanics

For each outbound delivery:

1. Generate `webhook-id` — UUIDv4.
2. Record `webhook-timestamp` — Unix epoch **seconds** as a decimal string.
3. Serialize the payload JSON to `body` bytes.
4. Signed content: `"{webhook-id}.{webhook-timestamp}.{body}"` (UTF-8 bytes).
5. Secret bytes: base64url-decode the content of `whsec_<base64url>` after stripping the prefix.
6. Signature: `base64.StdEncoding.EncodeToString(HMAC-SHA256(secretBytes, signedContent))`.
7. Header value: `"v1,<sig>"` — or `"v1,<sig1> v1,<sig2>"` when two secrets are active (rotation).

Required outbound headers:

```
webhook-id:        <uuid>
webhook-timestamp: <unix-epoch-seconds>
webhook-signature: v1,<base64-hmac> [v1,<base64-hmac2>]
Content-Type:      application/json
User-Agent:        SolidPing/1.0
```

Custom headers from `Channel.Settings["headers"]` are added after, so they can override `User-Agent`
but not the Standard Webhooks headers.

## Signing secret lifecycle

### Format

`whsec_<base64url-unpadded-32-bytes>` — same convention as Svix.

Generate with:

```go
raw := make([]byte, 32)
_, _ = crypto_rand.Read(raw)
secret := "whsec_" + base64.RawURLEncoding.EncodeToString(raw)
```

### Storage

Both the active secret and the previous secret (during rotation) are **always stored encrypted** via
the existing `settings_private` / `settings_private_keys` mechanism — even when no master key is
configured (fallback to plaintext per existing system behaviour). They never appear in the public
`Settings` JSONB.

```
settings_private keys: ["signingSecret", "signingSecretPrevious"]
settings (public):     {"url": "...", "headers": {...}, "signingSecretPreviousExpiry": "2026-05-26T10:00:00Z"}
```

`signingSecretPrevious` and `signingSecretPreviousExpiry` are null when not rotating.

### Auto-generation

- `CreateChannel` for type `webhook`: always generate `signingSecret`.
- Existing channels without a secret: auto-generate and persist on the **first `Send()` call** that
  finds no secret, then proceed with the newly generated secret. Log at INFO:
  `"auto-generated signing secret for webhook channel <uid>"`.

### Rotation

`POST /api/v1/orgs/:org/channels/:uid/rotate-secret` (no request body):

1. Assert channel type is `webhook`; 400 otherwise.
2. Move `signingSecret` → `signingSecretPrevious`.
3. Set `signingSecretPreviousExpiry` = now + 24 h.
4. Generate new `signingSecret`.
5. Return the updated `ChannelResponse`.

Background expiry: when `signingSecretPreviousExpiry` is in the past, clear both
`signingSecretPrevious` and `signingSecretPreviousExpiry` lazily on the next `Send()` call (check
before building the signature list). No dedicated cron job needed for V1.

## New payload format

Replace the flat map with a typed envelope:

```go
type WebhookPayload struct {
    Type      string          `json:"type"`
    Timestamp time.Time       `json:"timestamp"`
    Data      WebhookData     `json:"data"`
}

type WebhookData struct {
    Incident WebhookIncident `json:"incident"`
    Check    WebhookCheck    `json:"check"`
}

type WebhookIncident struct {
    UID                   string     `json:"uid"`
    StartedAt             time.Time  `json:"startedAt"`
    ResolvedAt            *time.Time `json:"resolvedAt"`
    DurationSeconds       *int       `json:"durationSeconds"`
    Title                 *string    `json:"title"`
    FailureCount          int        `json:"failureCount"`
    RelapseCount          int        `json:"relapseCount"`
    RecoveryPeriodSeconds *int       `json:"recoveryPeriodSeconds"`
}

type WebhookCheck struct {
    UID  string  `json:"uid"`
    Name string  `json:"name"`
    Type string  `json:"type"`
}
```

Example — incident created:

```json
{
  "type": "incident.created",
  "timestamp": "2026-05-25T10:00:00.000Z",
  "data": {
    "incident": {
      "uid": "018e4a2b-...",
      "startedAt": "2026-05-25T09:58:00Z",
      "resolvedAt": null,
      "durationSeconds": null,
      "title": "api.example.com → HTTP 500",
      "failureCount": 3,
      "relapseCount": 0,
      "recoveryPeriodSeconds": null
    },
    "check": {
      "uid": "018e4a1c-...",
      "name": "API health",
      "type": "http"
    }
  }
}
```

Event type strings (field `type`, previously `eventType`):
- `incident.created`
- `incident.resolved`
- `incident.escalated`

`WebhookCheck.Name` is `check.Name` if set, else `check.Slug` (existing fallback logic preserved).
`WebhookIncident.RecoveryPeriodSeconds` is omitted (null) when `RelapseCount == 0`.

## Test webhook endpoint

`POST /api/v1/orgs/:org/channels/:uid/test` (no request body):

Sends a synthetic signed webhook to the configured URL using the same path as a real delivery
(`WebhookSender.Send`), but with a fake `Payload` containing:
- `EventType`: `"incident.created"`
- A stub `Incident` (zero-UID, `StartedAt = now`, `FailureCount = 1`)
- A stub `Check` (zero-UID, `Name = "test-check"`, `Type = "http"`)

Response:

```json
{ "success": true,  "statusCode": 200, "durationMs": 42 }
{ "success": false, "statusCode": 500, "durationMs": 87, "error": "..." }
```

Always returns HTTP 200 — the caller inspects `success` to know if the remote accepted it.

## Backend implementation

### `server/internal/notifications/webhook.go`

Complete rewrite. Key changes:

- Replace `buildPayload()` return type from `map[string]any` to `WebhookPayload` struct (defined in
  this file).
- Add `signRequest(secrets []string, id, timestamp string, body []byte) string`:
  - For each `whsec_…` string: strip prefix, base64url-decode, compute `HMAC-SHA256` over
    `"{id}.{timestamp}.{body}"`, base64-encode, prefix with `"v1,"`.
  - Join with `" "`.
- `Send()` additions (after marshalling body):
  1. Extract/auto-generate secrets via `ensureSecrets(ctx, payload.Connection)` — a helper that
     reads from `SettingsPrivate`, generates if absent, and persists via a provided `ChannelUpdater`
     func (injected into `WebhookSender`).
  2. Purge expired previous secret (if `signingSecretPreviousExpiry` < now).
  3. Build active secrets slice (1 or 2 elements).
  4. Generate `webhook-id` (UUID), `webhook-timestamp` (epoch string).
  5. Set the 3 Standard Webhooks headers.
  6. Store `webhook-id` as the `MessageId` in `IncidentNotification` — pass it back via a return
     value or a field on `Payload`.

`WebhookSender` struct gains a `ChannelUpdater` dependency:

```go
type WebhookSender struct {
    UpdateChannel func(ctx context.Context, channel *models.Channel) error
}
```

Injected in the notification registry factory. When `UpdateChannel` is nil (e.g. tests that don't
need persistence), auto-generation is skipped and Send logs a warning.

### `server/internal/handlers/channels/service.go`

- `CreateChannel` — when type is `webhook`, call `generateWebhookSecret()` and add it to
  `settings_private_keys` + `SettingsPrivate`.
- New `RotateWebhookSecret(ctx, orgUID, channelUID string) (*ChannelResponse, error)`:
  implements the rotation logic described above.
- New `TestWebhookChannel(ctx, orgUID, channelUID string) (*WebhookTestResult, error)`:
  builds a stub payload, calls `WebhookSender.Send`, measures duration, returns result struct.

`WebhookTestResult`:

```go
type WebhookTestResult struct {
    Success    bool   `json:"success"`
    StatusCode int    `json:"statusCode"`
    DurationMs int64  `json:"durationMs"`
    Error      string `json:"error,omitempty"`
}
```

### `server/internal/handlers/channels/handler.go`

Add two handler methods:

```go
func (h *Handler) RotateWebhookSecret(w http.ResponseWriter, req bunrouter.Request) error
func (h *Handler) TestWebhookChannel(w http.ResponseWriter, req bunrouter.Request) error
```

Both extract `org` and `uid` params, delegate to service, return JSON.

### `server/internal/app/server.go`

Register two new routes under the existing channel group:

```go
channels.POST("/:uid/rotate-secret", channelsHandler.RotateWebhookSecret)
channels.POST("/:uid/test",          channelsHandler.TestWebhookChannel)
```

## Frontend implementation (`web/dash0`)

### `src/api/hooks.ts`

Add two mutations:

```typescript
useRotateWebhookSecret(org: string, channelUid: string)
// POST /api/v1/orgs/:org/channels/:uid/rotate-secret
// invalidates channel query on success

useTestWebhookChannel(org: string)
// POST /api/v1/orgs/:org/channels/:uid/test
// returns { success, statusCode, durationMs, error? }
```

`ChannelResponse` gains an optional `signingSecretPreviousExpiry?: string` field in settings.

### `src/components/channels/channel-form.tsx`

Webhook-specific section (visible when `channel.type === "webhook"`):

1. **Signing secret display** — monospace read-only input showing the secret from
   `channel.settings.signingSecret` (returned decrypted by the API for authorized users).
   Buttons: "Copy" (clipboard), "Rotate" (calls `useRotateWebhookSecret` mutation).
   - If secret is absent (legacy channel not yet auto-migrated), show: *"No signing secret — will be
     auto-generated on next delivery. Rotate to generate one now."*

2. **Rotation in-progress banner** — shown when `signingSecretPreviousExpiry` is set:
   *"Previous secret active until {date}. Remove it early by rotating again."*

3. **Test webhook button** — "Send test" calls `useTestWebhookChannel`. Shows inline result:
   - Success: green badge "200 OK · 42 ms"
   - Failure: red badge "500 · 87 ms — {error message}"

The signing secret is always retrievable (not a one-time reveal), so no special post-creation modal
is needed; the channel detail page shows it whenever the user navigates there.

## Files to create / modify

### Modified Go files
- `server/internal/notifications/webhook.go` — full rewrite: struct types, signing, new payload
- `server/internal/handlers/channels/service.go` — `CreateChannel`, `RotateWebhookSecret`,
  `TestWebhookChannel`
- `server/internal/handlers/channels/handler.go` — two new handler methods
- `server/internal/app/server.go` — two new routes

### Modified frontend files
- `web/dash0/src/api/hooks.ts` — two new mutations + `signingSecretPreviousExpiry` field
- `web/dash0/src/components/channels/channel-form.tsx` — signing secret section + test button

### No migrations needed
`settings_private` / `settings_private_keys` JSONB columns already exist on
`integration_connections`. No schema changes required.

## Tests

### `server/internal/notifications/webhook_test.go`

Table-driven, `t.Parallel()`, `testify/require`:

- `TestSignRequest_KnownVector`: hardcoded secret + id + timestamp + body → exact expected signature
  string. Cross-check against the Standard Webhooks reference implementation vectors.
- `TestSignRequest_TwoSecrets`: two secrets → header contains two space-separated `v1,…` entries.
- `TestWebhookSender_Send_SetsHeaders`: mock HTTP server captures request; assert `webhook-id`
  (UUID format), `webhook-timestamp` (numeric), `webhook-signature` (`v1,…`), `Content-Type:
  application/json`.
- `TestWebhookSender_Send_PayloadShape`: mock server captures body; assert `type`, `timestamp`,
  `data.incident.uid`, `data.check.name` fields.
- `TestWebhookSender_Send_AutoGeneratesSecret`: `UpdateChannel` callback is called when no secret
  exists; subsequent mock request carries a valid signature.
- `TestWebhookSender_Send_ExpiredPreviousSecretPurged`: rotation in progress, expiry in the past →
  only one signature in header; `UpdateChannel` called to clear previous.

### `server/internal/handlers/channels/service_test.go`

- `TestCreateWebhookChannel_GeneratesSecret`: created channel has `signingSecret` in
  `SettingsPrivateKeys` and a non-empty decrypted value prefixed `whsec_`.
- `TestRotateWebhookSecret_CyclesSecrets`: old secret moves to `signingSecretPrevious`; new secret
  is different; expiry ~24 h from now.
- `TestRotateWebhookSecret_WrongType`: non-webhook channel → 400.
- `TestTestWebhookChannel_Success` / `_Failure`: mock HTTP target; assert result fields.

### `web/dash0/e2e/`

Add `channels-webhook.spec.ts`:

- Create webhook channel → signing secret field visible and non-empty.
- Copy button copies to clipboard (use `page.evaluate` to read clipboard).
- Rotate → new secret displayed; rotation banner visible.
- "Send test" button → success badge rendered (mock endpoint via Playwright route intercept).

## Verification

```bash
make lint && make gotest && make build
```

API smoke test (needs running `make dev-test`):

```bash
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"test","email":"test@test.com","password":"test"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

# Create webhook channel
CH=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"type":"webhook","name":"my-hook","settings":{"url":"https://httpbin.org/post"}}' \
  'http://localhost:4000/api/v1/orgs/test/channels' | jq -r '.uid')

# Verify signing secret was auto-generated
curl -s -H "Authorization: Bearer $TOKEN" \
  "http://localhost:4000/api/v1/orgs/test/channels/$CH" | jq '.settings.signingSecret'

# Test the webhook (should return {"success":true,...})
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  "http://localhost:4000/api/v1/orgs/test/channels/$CH/test" | jq '.'

# Rotate the secret
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  "http://localhost:4000/api/v1/orgs/test/channels/$CH/rotate-secret" | jq '.settings.signingSecret'
```

For Standard Webhooks signature verification, use the reference implementation at
`https://github.com/standard-webhooks/standard-webhooks` to cross-check test vectors.

## Implementation Plan

This plan is derived from the spec's own "Implementation plan (commit-by-commit)" section,
adapted to the actual code layout discovered during exploration.

Key integration facts discovered:
- `webhook` connection secret fields live in `credentials.connectionSecretFields` and must gain
  `signingSecret` + `signingSecretPrevious` so they are encrypted at rest and stripped from the
  public `Settings` response (per existing split/encrypt machinery).
- The notification job runner (`job_notification.go`) already decrypts `SettingsPrivate` and merges
  it into `Connection.Settings` before calling `Send()`. So `WebhookSender.Send` reads decrypted
  secrets straight off `payload.Connection.Settings`.
- `WebhookSender` is currently a stateless struct returned by `notifications.GetSender`. The
  `UpdateChannel` callback is injected at the job-runner site (it has `jctx.DBService` +
  `jctx.Services.Credentials`), and the channels `Service` builds its own callback for the test
  endpoint.

Steps:

1. **Secret registry**: add `signingSecret` + `signingSecretPrevious` to the webhook entry in
   `conn_secrets.go`. Commit: `feat: encrypt webhook signing secrets at rest`.

2. **`webhook.go` rewrite**: typed payload structs (`WebhookPayload`/`WebhookData`/
   `WebhookIncident`/`WebhookCheck`), `generateWebhookSecret()`, `signRequest()`, `ensureSecrets()`,
   expired-previous purge, the three Standard Webhooks headers, `UpdateChannel` field on
   `WebhookSender`. Unit tests in `webhook_test.go`. Wire `UpdateChannel` callback in
   `job_notification.go`. Commit: `feat: rework webhook sender to standard-webhooks signing and typed payload`.

3. **Secret lifecycle in service**: `CreateChannel` webhook auto-generate, `RotateWebhookSecret`,
   `generateWebhookSecret` helper, expose decrypted `signingSecret`/`signingSecretPrevious`/
   `signingSecretPreviousExpiry` in `GetChannel` response. Service tests. Commit:
   `feat: add webhook signing-secret generation and rotation`.

4. **Test endpoint**: `TestWebhookChannel` service method + `WebhookTestResult`, handler methods
   (`RotateWebhookSecret`, `TestWebhookChannel`), two routes in `server.go`. Service tests. Commit:
   `feat: add test-webhook endpoint`.

5. **Frontend**: `hooks.ts` mutations (`useRotateWebhookSecret`, `useTestWebhookChannel`) +
   `signingSecretPreviousExpiry` field; channel-form signing-secret section (display/copy/rotate +
   rotation banner + send-test). Commit:
   `feat: show webhook signing secret and test/rotate in channel form`.

6. **E2E**: `web/dash0/e2e/channels-webhook.spec.ts`. Commit: `test: add webhook channel e2e tests`.

7. QA: `make build-backend build-dash0 lint-back test`; fix until green.

## Implementation plan (commit-by-commit)

1. **`webhook.go` rewrite**: `WebhookPayload`/`WebhookData`/`WebhookIncident`/`WebhookCheck`
   structs, `signRequest()`, updated `buildPayload()`, signing in `Send()`, `UpdateChannel` field.
   Unit tests for signing in `webhook_test.go`.
   Commit: `feat: rework webhook sender to standard-webhooks signing and typed payload`.

2. **Secret lifecycle in service**: `CreateChannel` auto-generate, `RotateWebhookSecret`,
   `generateWebhookSecret()` helper. Service tests.
   Commit: `feat: add webhook signing-secret generation and rotation`.

3. **Test endpoint**: `TestWebhookChannel` in service + handler + route.
   Commit: `feat: add test-webhook endpoint`.

4. **Frontend**: `hooks.ts` mutations + channel-form signing-secret section + test button.
   Commit: `feat: show webhook signing secret and test/rotate in channel form`.

5. **E2E Playwright tests**.
   Commit: `test: add webhook channel e2e tests`.

6. QA: `make lint && make gotest && make build`, then manual API + UI verification.
