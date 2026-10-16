---
model: opus
effort: medium
---

# Send status-page subscriber webhooks and publication webhooks through the egress guard

## Problem
Found by the security scan of 2026-10-14 (findings D2 and D3). Severity: high on SaaS, where it is a blind SSRF from the API/jobs process with a partial read-back. Low on self-hosted, where private targets are allowed by design. Who can trigger it: any member with role ≥ `user`, in their own org. D2 needs an endpoint subscriber on a status page, D3 a `webhook` integration. On SaaS, open signup means anyone: an attacker can create their own org (evilco).

Two outbound senders skip the egress guard that every other notification sender uses. The reference pattern is `notifications.WebhookSender`: `ValidateSenderURL` before the request, then `httpclientpool.NewGuardedClient(..., guard)` (`server/internal/notifications/webhook.go:156-196,239-274`).

**D2: status-page webhook/Slack subscribers.**
- **Client.** `statussubscribers.NewNotifier` builds a bare `&http.Client{Timeout: deliveryTimeout}` (`server/internal/handlers/statussubscribers/notifier.go:106-110`). That client has the default transport (no guard) and the default redirect policy, and `post` sends with it (`delivery.go:182-214`).
  - The three constructors all keep that client: `app/server.go:1310-1312`, `checkworker/worker.go:295-297`, `jobs/jobtypes/job_incident_publish.go:101-104`.
  - `SetHTTPClient` (`notifier.go:113-118`) is a test seam only.
- **Save-time check.** `normalizeEndpointURL` / `isLocalHost` (`channels.go:59-99`) only require https and refuse a literal prefix list (`localhost`, `127.0.0.1`, `0.0.0.0`, `::1`, `10.`, `192.168.`, `169.254.`). There is no DNS resolution.
  - It lets through 172.16/12, 100.64/10, `127.0.0.2`+, IPv6 ULA/link-local, `[::ffff:10.0.0.1]`, numeric hosts (`2130706433`), and any hostname that resolves to a private address.
  - Production never sets `WithAllowPrivateEndpoints` (`server.go:2036`), so this denylist is the only control, in every deployment mode.
- **Route.** `POST /orgs/:org/status-pages/:statusPageUid/subscribers/endpoints` (`server.go:2051-2052`, `orgGroup`).
- **Attack.**
  1. Register an https endpoint on an attacker host.
  2. Publish a status update.
  3. The host answers with a 307/302 to the metadata address or to a 10.x service, and Go follows it. A 307 keeps the POST body and the `X-SP-Signature` headers.
  - Without a redirect: a hostname that resolves privately, or an IPv6 literal, passes validation directly.
- **Read-back.** Small. After 5 consecutive failures, the `statuspage.subscriber.disabled` event stores `lastError`, which holds up to 512 bytes of the non-2xx body (`delivery.go:211-213,276`).

**D3: publication webhook fan-out job.**
- **Enqueue.** When a publication changes, `dispatchWebhooks` enqueues one `webhook` job per enabled `webhook` integration of the org (`handlers/incidentpublications/webhooks.go:108-129`).
- **Send.** `WebhookJobRun.Run` sends with a bare `&http.Client{Timeout: webhookTimeout}` (`jobs/jobtypes/job_webhook.go:98-102`). There is no send-time `ValidateSenderURL` and no guard, and redirects are followed.
- **Only checked at save.** The URL is validated only when the integration is saved (`handlers/integrations/service.go:698-737`, key from `senderURLSettingsKey` `:651-662`). `ValidateSenderURL` also lets an unresolvable name through (`notifications/urlvalidate.go:92-97`). So the job can still reach internal addresses through:
  - a redirect;
  - a later DNS change or rebind;
  - a row saved while the policy allowed private targets.
- **Read-back.**
  - `job_webhook.go:115` reads the whole body with `io.ReadAll`, with no limit. That is also a memory sink: a receiver can stream a large body for the full 30 s.
  - `:126-127` puts the entire body into the terminal error. `jobworker/worker.go:535-536` stores that error as job output `{"error": …}`.
  - `GET /orgs/:org/jobs` and `GET /orgs/:org/jobs/:uid` return it to every member, viewers included (`handlers/jobs/handler.go:159-219`, wired at `server.go:1107-1117`).
  - Network errors are retryable (`job_webhook.go:105`), and their text is stored as `{"error": …, "retried": true}` (`worker.go:551`). That gives a connect-refused vs timeout oracle on internal ports.
- **Only entry point.** `webhook` jobs cannot be created through `POST /orgs/:org/jobs`: that endpoint's allowlist is `sleep` only (`jobs/jobdef/types.go:230-232`). So this fan-out is the only way in.

## Proposal
1. **Notifier: use the guarded client and check the URL at send time.**
   - Add a `guard *egress.Guard` parameter to `statussubscribers.NewNotifier` (`notifier.go:98-111`), store it, and build `httpClient` with `httpclientpool.NewGuardedClient(deliveryTimeout, guard)`.
   - Make it a required parameter, not a setter, so none of the three constructors can forget it:
     - `server.go:1310`: pass `s.services.EgressGuard`;
     - `checkworker/worker.go:295`: pass `svc.EgressGuard`;
     - `job_incident_publish.go:101`: pass `jctx.Services.EgressGuard`, nil-safe.

     The registry guard is installed before routes and workers start (`server.go:4309-4336`), so it is set when these constructors run.
   - In `post` (`delivery.go:182`), call `notifications.ValidateSenderURL(ctx, n.guard, secrets.URL)` before building the request.
   - A refusal counts against the circuit breaker, like a non-2xx. It is a bad endpoint, not a local configuration fault (contrast `delivery.go:105-115`). So after 5 refusals the subscriber is disabled, and the event says why.
   - Keep `SetHTTPClient` as the test seam.
2. **Save-time validation: replace the prefix denylist with the shared validator.**
   - Add a `WithEgressGuard(guard)` option to `statussubscribers.Service` (`service.go:54-87`) and wire it at `server.go:2036` with `s.services.EgressGuard`.
   - In `normalizeEndpointURL` / `validateChannelEndpoint` (`channels.go:59-119`), keep the https-only rule and the Slack host rule. Then call `notifications.ValidateSenderURL(ctx, s.guard, normalized)`. Wrap a refusal so that both `ErrInvalidEndpointURL` and `notifications.ErrSenderURLInvalid` match it.
   - Delete `isLocalHost` and `allowPrivateEndpoints` / `WithAllowPrivateEndpoints`. Tests use `WithEgressGuard(egress.New(true))` instead.
   - This changes the self-hosted default: see open question 1.
3. **Publication webhook job.**
   - In `WebhookJobRun.Run` (`job_webhook.go:65-131`), take the guard from `jctx.Services.EgressGuard` through a nil-safe helper next to `credsFrom` (`job_kubernetes_discovery.go:157-163`). Spec 2026-10-16-10 adds the same helper, so reuse it if it already exists.
   - Call `notifications.ValidateSenderURL` before building the request, and send with `httpclientpool.NewGuardedClient(webhookTimeout, guard)`.
   - A refusal (`errors.Is(err, egress.ErrDenied)` or `ErrSenderURLInvalid`) is terminal, not retryable.
   - Do not switch this job to `notifications.WebhookSender`. That sender builds the incident payload (`buildPayload`) and signs it, so the publication payload receivers get today would change. Only the transport is wrong.
4. **Cap what is read and stored.**
   - At `job_webhook.go:115`, read through an `io.LimitReader` of 512 bytes, the same cap as `statussubscribers.maxErrorBodyBytes` (`delivery.go:37`).
   - Put only the status code and that snippet in the error (open question 2), and drain the rest with a bounded copy.
   - Bound the success-path drain at `delivery.go:205` the same way.
5. **Docs.**
   - `web/docs/docs/configuration/egress-policy.md:106-122`: add the status-page subscriber webhook/Slack URL and the publication webhook fan-out to the list of guarded senders.
   - `web/docs/docs/features/status-pages.md:783-800`: say that, under an enforcing policy, an endpoint must be public.

## Tests
- `server/internal/handlers/statussubscribers/egress_test.go` (new), table-driven:
  - **Save-time regression.** Build the service with `WithEgressGuard(egress.New(false, egress.WithLookup(stub)))`. `CreateEndpointSubscriber` must refuse each of these, with `errors.Is(err, ErrInvalidEndpointURL)`:
    - `https://172.16.0.5/h`, `https://100.64.0.1/h`, `https://127.0.0.2/h`;
    - `https://[fd00::1]/h`, `https://[::ffff:10.0.0.1]/h`, `https://2130706433/h`;
    - a hostname the stub resolves to `10.0.0.5`.

    Today's denylist accepts every one of them.
  - **Save-time positive control.** A hostname the stub resolves to a public address (e.g. `203.0.113.10`) is accepted. With `egress.New(true)`, `https://10.1.2.3/h` is accepted.
  - **Send-time regression.** Create a subscriber through a permissive service, with its envelope pointing at an httptest TLS server on 127.0.0.1. Build the notifier with `NewNotifier(..., egress.New(false))` and publish.
    - The server receives no request, and `FailureCount` becomes 1.
    - This exercises the production constructor. The refusal is pre-flight, so no TLS trust is needed.
  - **Redirect regression.**
    - Hop 1 is a TLS server on `[::1]`, treated as public through `egress.WithPublicOverride(net.ParseIP("::1"))`. It answers 307 to hop 2, on `http://127.0.0.1`.
    - Give the notifier a client through `SetHTTPClient`, with transport `guard.HTTPTransport().Clone()` plus hop 1's TLS roots. The clone keeps the guarded `DialContext`.
    - Hop 2 is never hit, and the delivery counts as failed. Before the fix, hop 2 receives the POST body.
  - **Positive control.** The same TLS server under `egress.New(false, egress.WithPublicOverride(127.0.0.1))` gets the delivery, signed.
- `server/internal/handlers/statussubscribers/delivery_test.go`:
  - `TestEndpointValidation` (`:306-344`) moves from the denylist to the guard.
  - The shared fixture (`service_test.go:40`) switches to `WithEgressGuard(egress.New(true))`.
  - `TestWebhookDeliveryIsSignedAndCarriesOnlyPublicFields` keeps passing.
- `server/internal/jobs/jobtypes/job_webhook_test.go` (new), table-driven:
  - **Regression.** Enforcing guard in `jctx.Services`, URL pointing at a loopback httptest server:
    - the error matches `egress.ErrDenied` / `ErrSenderURLInvalid`;
    - `jobdef.IsRetryable(err)` is false;
    - the server's hit count is 0.
  - **Redirect.** Hop 1 on `[::1]` (public override) answers 307 to hop 2 on 127.0.0.1. Hop 2 is never hit.
  - **Body cap.** The receiver answers 500 with a 1 MiB body ending in a marker. The error string is at most about 600 bytes, and the marker is absent.
  - **Positive control.** With a permissive guard (or `WithPublicOverride(127.0.0.1)`) and a 200 answer, the error is nil and the receiver got the JSON body. With `jctx.Services == nil` (no policy), the job still sends.

## To verify
- **Import cycle.** Check that `statussubscribers` importing `notifications` creates no import cycle. `notifications` imports `jobs/jobdef`, which imports `app/services`. If it does create one, move `ValidateSenderURL` into `egress` (or a small subpackage) and keep a thin wrapper in `notifications`.
- **Event visibility.** Check who can read `statuspage.subscriber.disabled` events through the events endpoints. The report says members can, which would make the 512-byte `lastError` member-visible.

## Open questions
1. **Self-hosted relaxation.**
   - Today the subscriber denylist applies in every deployment mode. Following the egress policy instead means a self-hosted instance (permissive by default) would accept `https://10.x` and LAN hostnames for subscriber webhooks, as it already does for `webhook` integrations.
   - Should we follow the policy, or keep a hard floor for loopback?
   - Recommended: follow the policy. That leaves one operator knob (`SP_EGRESS_ALLOW_PRIVATE`), the same as for every other sender.
2. **Body in the job output.**
   - Keep a 512-byte snippet of a failing receiver's body in the stored job error, or store only the status code?
   - Recommended: keep the 512-byte snippet. With the guard in place, it can only come from a public host the org chose, and it is what makes a failing receiver debuggable.
3. **Redirects.**
   - Keep following redirects, or refuse them for these two senders? If followed, the guarded dial re-checks each hop, as in `notifications.WebhookSender`.
   - Recommended: keep following them, guarded, for consistency with the other senders.

## Related
M1 (2026-10-11): the MS Teams and Zulip senders have the same unguarded-client flaw, in separate code.
