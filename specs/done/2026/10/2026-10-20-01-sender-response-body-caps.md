---
model: opus
effort: medium
---

# Notification senders and the RabbitMQ checker read responses from user-chosen hosts with no size cap: a user's own server can exhaust the server's memory

## Problem

Found by the security scan of 2026-10-20 (finding K1). Severity: medium. Any member with write access to an org (role `user` or above) can trigger it, because notification integrations and checks are self-service. On SaaS that is anyone: any account can create its own org.

This is the same flaw as J1 (spec `specs/done/2026/10/2026-10-19-01-a2a-response-size-cap.md`), which fixed the A2A sender only. The other senders that talk to a URL the user picks still read the response body whole:

| Call site | Host chosen by the user | When the body is read |
|---|---|---|
| `server/internal/notifications/googlechat.go:67` | webhook URL | status >= 300 |
| `server/internal/notifications/gotify.go:102` | server URL | status >= 300 |
| `server/internal/notifications/matrix.go:106` (send) | homeserver URL | always |
| `server/internal/notifications/matrix.go:184` (alias resolve) | homeserver URL | always |
| `server/internal/notifications/mattermost.go:85` | webhook URL | status >= 300 |
| `server/internal/notifications/msteams.go:86` | webhook URL | status >= 300 |
| `server/internal/notifications/ntfy.go:75` | server URL | status >= 300 |
| `server/internal/notifications/slackwebhook.go:74` | webhook URL | status >= 300 |
| `server/internal/notifications/zulip.go:107` | site URL | always |
| `server/internal/checkers/checkrabbitmq/checker.go:383` (`/api/nodes`) | management URL | always |
| `server/internal/checkers/checkrabbitmq/checker.go:652` (overview) | management URL | always |

- Each one is a bare `io.ReadAll(resp.Body)`.
  - The only bound is the client timeout: 30 s for every sender (`googleChatTimeout`, `gotifyTimeout`, ...), and the check timeout for RabbitMQ. A fast server sends gigabytes in that time.
  - The egress guard does not help: the attacker's server is a public host.
- The senders also paste the whole body into the returned error (`fmt.Errorf("...: %s", string(respBody))`). So the buffer is copied at least once more, and it ends up in the test result returned to the caller and in the delivery/job error.
- The capped senders show the intended pattern:
  - `webhook.go:264` (`models.DeliveryDetailsBodyCap`, 16 KiB);
  - `discord_webhook.go:139` (`discordWebhookBodySnippet`, 512 B);
  - `a2a.go:212` (`a2aMaxErrorBody`) and `a2aCappedBody` (J1);
  - `checkhttp` (10 MB).
- Reachable paths:
  - `POST /orgs/:org/integrations/:uid/test` (`app/server.go:1945`, `integrations/service.go:1731` `TestIntegration` → `sender.Send`). It runs synchronously on the API node, with no rate limit.
  - Notification jobs on the workers, at every incident event.
  - A RabbitMQ check on the check workers, at every period. The shortest period makes it the most repeatable path.
- `pagerduty.go:322` and `pushover.go:101` read uncapped too, but they talk to fixed vendor URLs (`pagerdutyEventsURL`, `pushoverAPIURL`), not a user-chosen host. Cap them anyway for consistency, but they are not the attack path.
- Impact: a server that answers with an endless body (error status for most senders, any status for Matrix, Zulip and RabbitMQ) makes the process buffer it until the timeout. A few parallel test calls, or a handful of checks on short periods, are enough to OOM-kill a pod. When the API and the workers share a process (the single-binary deployment), checks and alerts stop for every org until the restart, and the attacker can repeat it at will.

## Proposal

1. Add one helper in `server/internal/notifications` (e.g. `readCappedBody(resp *http.Response, limit int64) []byte`) that reads through `io.LimitReader` and marks truncation. Use it at the nine sender call sites above, plus `pagerduty.go:322` and `pushover.go:101`.
   - The senders only need the body for an error snippet or a small JSON answer.
   - Matrix and Zulip parse a small JSON answer, so a few KiB is enough. Reuse `models.DeliveryDetailsBodyCap` (16 KiB) as the cap for all of them, so the error snippet matches what the webhook sender already stores.
   - Snippets that go into an error string should stay short: keep the 512 B `discordWebhookBodySnippet` style for the error text.
2. In `checkrabbitmq/checker.go:383` and `:652`, read through `io.LimitReader` with a cap that fits a real management API answer (1 MiB is ample for `/api/nodes` and `/api/overview`). Over the cap, fail with a clear "management API response too large" error instead of parsing a truncated body.
3. Grep `server/internal` for any other `io.ReadAll(resp.Body)` whose host comes from user settings, and cap it the same way. Known candidate: `integrations/freebox/client.go:266`, whose `baseURL` comes from the integration. Calls to fixed provider APIs (OAuth providers, Slack/Twilio/WhatsApp/OVH APIs) are out of scope.

## Tests

- `server/internal/notifications/egress_test.go` (or a new `body_cap_test.go`), table-driven over every sender listed above:
  - An `httptest` server answers with an error status (`200` for Matrix and Zulip) and streams a body far larger than the cap.
  - The server counts the bytes it managed to write before the client stopped reading.
  - Assert that `Send` returns an error, that the byte count stays below cap + transport buffering margin, and that the error string is no longer than the snippet cap.
  - This fails before the fix: the client reads the whole stream.
- Positive control in the same table: a normal error answer (a short JSON error) still produces an error that contains the server's message. For Matrix and Zulip, a normal success JSON still parses and `Send` succeeds.
- `server/internal/checkers/checkrabbitmq/checker_test.go`: a management endpoint returning an oversized `200` body makes the check fail with the "too large" error. The byte count stays bounded. Positive control: a normal `/api/overview` and `/api/nodes` answer still passes.

## To verify

- Whether a prod API pod also runs the job and check workers (single binary). That decides whether an OOM stops monitoring or only the API.
- Whether `integrations/freebox/client.go`'s `baseURL` is user-controlled on every path, or only on the pairing flow (known T5 gates who can pair).
