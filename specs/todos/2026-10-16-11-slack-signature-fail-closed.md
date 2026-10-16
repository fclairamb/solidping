---
model: opus
effort: medium
---

# Slack inbound endpoints: fail closed without a signing secret, allow-list `response_url`, cap the body

## Problem
Found by the security scan of 2026-10-11 (finding H2). **High: an unauthenticated attacker can read and change monitoring data, and the server can be made to send blind requests (SSRF).** It works on every instance where no Slack signing secret is set, whatever the deployment mode. The 2026-10-15 scan found the `response_url` SSRF again (in its `ssrf2` run) and filed it under H2, so it is folded in here.

**Fail-open verification.**
- `VerifyMiddleware` (`server/internal/integrations/slack/verify.go:30-37`) calls `next` without any check when `cfg.Slack.SigningSecret` is empty.
- The secret has no default (`config.go:1429`, `systemconfig.go:692-700`, `SP_SLACK_SIGNING_SECRET`).
- The three routes are registered unconditionally, even when Slack is disabled (`server/internal/app/server.go:2217-2224`):
  - `POST /api/v1/integrations/slack/events`
  - `POST /api/v1/integrations/slack/command`
  - `POST /api/v1/integrations/slack/interaction`
- So the endpoints are open on three kinds of instance:
  - every self-hosted instance that never configured Slack;
  - every Socket Mode install, which has no reason to set the secret because Slack delivers over the WebSocket (`config.go:1431-1434`);
  - any HTTP-mode install that forgot the secret.

**What a forged request can do:**
- **With a known `team_id`** (not a secret):
  - `/solidping checks list|incidents|results|config` returns org data in the response;
  - `checks add` / `rm` create or delete checks (`mention_commands.go:29`, `:59`);
  - block actions acknowledge or escalate incidents (`interactions.go:71-76`).
- **With no Slack connection at all:** `/solidping check <url>` ACKs and spawns `finishCheckCommandAsync` (`commands.go:88-108`, `:135-147`).
  - The create step fails with `ErrConnectionNotFound`.
  - The error message is still POSTed to the caller-supplied `response_url` (`commands.go:143`).
  - That POST goes through `responseURLHTTPClient = httpclientpool.NewClient(10s)` (`response_url.go:20-25`, `:31-55`): no egress guard, no host allow-list, and the default redirect policy.
  - Result: a blind SSRF from the API process to any URL, internal addresses and redirect hops included.
  - The Socket Mode path feeds the same function (`socketmode.go:396`), but there the URL comes from Slack.

**Unbounded body.** When a secret *is* set, `io.ReadAll(req.Body)` (`verify.go:66`) buffers the whole body before the signature is checked.

**Minor.** The freshness check is one-sided (`verify.go:53`): a far-future timestamp passes. Discord rejects both directions (`discord/verify.go:107-125`).

**Discord already does it right.** `discord/verify.go:33-59` refuses every request when no key is configured, and caps the body at 1 MiB (`:28-30`, `:74`).

**Not covered here.** H2 also notes that a command is not mapped to an org member. Once signatures are mandatory, only real members of the linked workspace can send commands. Whether every workspace member may create or delete checks is a product question, outside this fix.

## Proposal
1. **Fail closed** in `VerifyMiddleware` (`verify.go:30-37`).
   - When `h.cfg == nil` or `h.cfg.Slack.SigningSecret == ""`, answer `401 UNAUTHORIZED` "Slack signature verification is not configured" and never call `next`. Guard a nil `cfg`, as `discord/verify.go:50-53` does: `DispatchCommand` builds `&Handler{svc: svc}` with no `cfg` (`commands.go:31`).
   - Replace the doc comment with the rationale Discord's has (`discord/verify.go:33-47`).
2. **Socket Mode is unaffected, by construction.**
   - The supervisor dispatches straight into `DispatchEvent` / `DispatchCommand` / `DispatchInteraction` (`socketmode.go:236`, `:304-330`) and never passes through `VerifyMiddleware`. Its trust anchor is the `xapp-` app token.
   - Slack delivers to exactly one transport (`config.go:1431-1433`), so on a Socket Mode install the HTTP routes are never called. Answering 401 there is correct.
   - Keep the routes registered: a 401 is a clearer signal than a 404 for an operator who wired the Request URLs but forgot the secret.
3. **Cap the body** before the signature is checked (`verify.go:65-70`).
   - Use `req.Body = http.MaxBytesReader(writer, req.Body, maxSlackBody)` with `maxSlackBody = 1 << 20`, the same size as Discord's `maxInteractionBody`.
   - Map `*http.MaxBytesError` to 413, as the heartbeat handler does (`handlers/heartbeat/handler.go:76-83`).
   - Keep the header checks before the read, as today, so a request with no headers never reads the body.
4. **Two-sided timestamp freshness.** Replace `verify.go:46-56` with an absolute-age check like `discord/verify.go:107-125`, written locally. An integration package should not import another.
5. **Allow-list `response_url`** in `response_url.go`.
   - Add `validateResponseURL(raw string) error`. It accepts only:
     - scheme `https`;
     - host equal (case-insensitive) to `hooks.slack.com`;
     - no userinfo;
     - no explicit port.
   - Mirror `validateSlackWebhookURL` (`handlers/integrations/service.go:664-686`). The slack package cannot import it, because `handlers/integrations` imports `slack` (see `server.go:2198-2201`).
   - Call it first thing in `postResponseURL` (`:31`), and send nothing on failure.
   - In `handleCheckCommand` (`commands.go:88-96`), treat a present-but-invalid `response_url` like a missing one: answer synchronously, with no outbound call. Log a WARN with `team_id`.
   - Give `responseURLHTTPClient` a `CheckRedirect` that returns `http.ErrUseLastResponse`, so the allowed host cannot bounce the POST elsewhere.
   - To make the client injectable in tests, split it into `postResponseURLWith(ctx, client, url, msg)`.
6. **Make the config change visible.**
   - At route registration (`server.go:2217`), log a WARN when `Slack.Enabled && !Slack.SocketModeEnabled && Slack.SigningSecret == ""`: the HTTP endpoints will reject every request until `SP_SLACK_SIGNING_SECRET` is set.
   - In `web/docs/docs/configuration/notifications.md:131-152`, say that the signing secret is **required** for HTTP delivery (Event Subscriptions, Interactivity and Slash Command Request URLs), and that Socket Mode installs (`SP_SLACK_SOCKET_MODE_ENABLED` + `SP_SLACK_APP_TOKEN`) do not need it.
   - Add a `CHANGELOG.md` entry: security fix, plus a behaviour change for HTTP-mode installs with no secret.

## Tests
- **New `server/internal/integrations/slack/verify_test.go`**, modelled on `discord/verify_test.go`. A `signedRequest` helper computes `v0=HMAC-SHA256(secret, "v0:"+ts+":"+body)`. Table-driven:
  - **Regression:** an empty secret, and a nil `cfg`, give 401 and `next` is not reached. This fails before the fix: today the request passes through.
  - **Positive control:** a valid signature reaches `next`, and the downstream handler reads the identical body.
  - Wrong secret, tampered body, missing timestamp, missing signature, stale timestamp (now−301 s): 401.
  - Far-future timestamp (now+301 s): 401. This fails before the fix.
  - A signed body of `maxSlackBody+1` bytes gives 413, with `next` not reached. A signed body of exactly `maxSlackBody` bytes passes.
- **New `server/internal/integrations/slack/response_url_test.go`:**
  - `validateResponseURL` table:
    - Accept: `https://hooks.slack.com/commands/T0/1/x`, `https://HOOKS.slack.com/actions/T0/1/x`.
    - Reject: `http://hooks.slack.com/...`, `https://hooks.slack.com.evil.example/`, `https://evil.example/hooks.slack.com`, userinfo, `:8443`, an IP literal, the empty string.
  - **Regression:** `postResponseURL` with an `httptest` server URL returns an error, and the server receives zero requests. Before the fix it receives the POST.
  - **Positive control:** the injected client uses a recording `RoundTripper` (no network). A valid hooks.slack.com URL gets exactly one POST with the JSON message.
  - **Redirect:** the stub answers 302 to `http://127.0.0.1/x`. The client does not follow it: one request recorded.
- **Command path** (`commands_test.go`, new): `/solidping check https://example.com` with `response_url=https://attacker.example/x` gets a synchronous reply, and the recording transport sees no request.
- **Socket Mode control:** extend `TestSocketSupervisor_AcksSlashCommandAndCallsDispatcher` (`socketmode_test.go:191`) to run with an empty `SigningSecret` and assert the dispatcher is still called.

## To verify
- Before deploying, check that every HTTP-mode deployment sets `SP_SLACK_SIGNING_SECRET`: prod and `solidping.k8xp.com` (from the `k8xp` repo, read-only). Otherwise their Slack commands and buttons stop working. The 2026-10-11 report asked for this check and it was never answered.
- Whether a signing secret set later from Server settings reaches `h.cfg` without a restart (the systemconfig overlay, `systemconfig.go:692-700`). If it does not, the boot WARN must say a restart is needed.

## Open questions
1. **GovSlack** (`hooks.slack-gov.com`) in the allow-list? Recommended: no. Nothing else in the integration supports GovSlack (`client.go:29-31` pins `slack.com`).

## Related
D5 (2026-10-14, no global body cap; this spec only caps the Slack routes), L4 (2026-10-11, unauthenticated `/integrations/slack/socket/status`), M1 / D2 / D3 (other outbound clients without the egress guard).
