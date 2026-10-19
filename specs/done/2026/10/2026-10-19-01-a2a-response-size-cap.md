---
model: opus
effort: medium
---

# A2A client reads Agent Card and JSON-RPC responses with no size cap: a user's own agent can exhaust the server's memory

## Problem

Found by the security scan of 2026-10-19 (finding J1). Severity: medium. Any member with write access to an org (role `user` or above) can trigger it, since notification integrations are self-service. On SaaS that is anyone: any account can create its own org.

- The A2A channel (spec `2026-10-18-02`, `server/internal/notifications/a2a.go`) talks to a URL the user picks, through the `a2a-go/v2` v2.6.0 SDK. The SDK reads every response body whole, with no size limit:
  - Agent Card: `agentcard.Resolver` (`a2a.go:340`) ends in the SDK's `fetchCard`, which does `io.ReadAll(resp.Body)` (`a2aclient/agentcard/resolver.go:173` in the module).
  - `SendMessage` (`a2a.go:494`) and `GetTask` (`a2a.go:849`): the SDK's `jsonrpcTransport.sendRequest` decodes the body with `json.NewDecoder(httpResp.Body).Decode(&resp)` (`a2aclient/jsonrpc.go:129`). `resp.Result` is a `json.RawMessage`, so the whole result is buffered. The `a2av0` compat client does the same (`a2acompat/a2av0/jsonrpc_client.go:128`).
- On the SolidPing side, nothing limits the body either:
  - `a2aHTTPClient` (`a2a.go:213`) is the guarded client plus `a2aStatusTransport` (`a2a.go:192`). The transport caps only error bodies (`a2aMaxErrorBody`, 512 bytes, `a2a.go:58`). A `200` body goes to the SDK untouched.
  - The only bound is the 30 s client timeout (`a2aTimeout`, `a2a.go:52`). It covers reading the body, but a fast server can send several GB in 30 s.
  - Every other sender caps what it reads: `webhook.go:264` (`models.DeliveryDetailsBodyCap`), `discord_webhook.go:139`, and `checkhttp` (`checker.go:31`, 10 MB).
- The egress guard does not help: the attacker's server is a public host.
- Reachable paths:
  - `POST /orgs/:org/integrations/a2a/resolve-card` (`handlers/integrations/a2a.go:38`) and `POST /orgs/:org/integrations/:uid/test` (`testA2AConnection`, `a2a.go:90`). Both run synchronously on the API node, with no rate limit, and need nothing saved beyond the form values.
  - The notification job (`A2ASender.Send`) and the `a2a_task_poll` chain, which runs every 60 s for up to 30 min per task, on the job workers.
- Impact: a server that answers the card request with an endless `200` JSON body makes the process buffer it until the timeout. A few parallel calls are enough to OOM-kill a pod. When the API and the workers share a process (the single-binary deployment), checks and alerts stop for every org until the restart, and the attacker can repeat it at will.

## Proposal

1. In `server/internal/notifications/a2a.go`, cap every A2A response body inside `a2aStatusTransport.RoundTrip`. For a status below 400, wrap `resp.Body` in a reader that returns an error after a byte cap. The error must not look like EOF: a truncated card has to fail, not parse.
   - Add a const `a2aMaxResponseBody`: 1 MiB is plenty for a card or a task with artifacts.
   - Since every request (card, `SendMessage`, `GetTask`, both codecs) goes through `a2aHTTPClient`, this one change covers all paths. The SDK cannot bypass it: `WithDefaultsDisabled` plus the two explicit JSON-RPC transports leave no other client in use.
2. Classify the "response too large" error as permanent in `classifyA2AError` (a dedicated sentinel, e.g. `ErrA2AResponseTooLarge`). Retrying the same oversized agent is pointless.
3. Check that `resolve-card` / test return a readable error ("agent response exceeds 1 MiB") instead of a JSON parse error.

## Tests

`server/internal/notifications/a2a_test.go`, next to `TestA2ASender_BadCardsArePermanent`:
- Card too large: an `httptest` server answers the card URL with a `200`, `Content-Type: application/json`, and a body larger than the cap (a valid JSON prefix followed by padding, streamed). `ResolveA2AAgent` must fail with `ErrA2AResponseTooLarge`, and the handler must have read at most cap + a small margin. Count the bytes written by the server. This test fails before the fix: the read succeeds or fails only on parsing, after reading everything.
- `SendMessage` result too large: a valid card, then a JSON-RPC endpoint whose `200` response is larger than the cap. `Send` must return a permanent (non-retryable) error.
- Positive control: a card and a `SendMessage` answer just under the cap (e.g. a task whose artifact text is close to 1 MiB) still go through. The comment is recorded, truncated to `MaxCommentLength` as today.
- Keep `TestA2ASender_ErrorClassification` green. Error bodies stay capped at 512 bytes.

## To verify

- Whether `json.Decoder` reports the capped reader's error, rather than `unexpected EOF`, so that classification can use `errors.Is`. If not, have the reader record that it hit the cap and check that flag.
- Whether a prod API pod also runs the job workers (single binary). That decides whether an OOM stops monitoring or only the API.
