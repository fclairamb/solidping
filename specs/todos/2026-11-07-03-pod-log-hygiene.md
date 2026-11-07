---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# Server and agent logs: unstructured stdlib/chromedp lines at INFO, a WARN on every shutdown, a duplicated attribute, a startup WARN on every node

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/51

## Source

A review of a day of production and dev pod logs found five small log-hygiene defects. Each is minor; together they put noise in the INFO/WARN streams and make grouping impossible for two of them (the variable part is in the message).

## Problem

1. **TLS handshake errors from the in-server TLS edge.** `tlsedge.Edge.Start` builds `httpsSrv` (`server/internal/tlsedge/edge.go:491-496`) and `httpSrv` (`:485-489`) with no `ErrorLog`, so `net/http` writes `http: TLS handshake error from <ip>:<port>: tlsedge: hostname not allowed for certificate issuance: <host>` through the stdlib `log` package, which the slog bridge emits at INFO. Client address and SNI host are inside the message: ~1,300 lines/day, hundreds of distinct messages, mostly scanners.
2. **chromedp error lines at INFO on the agent.** `browserContext` (`server/internal/checkers/checkbrowser/checker.go:515-521`) calls `chromedp.NewContext` without `chromedp.WithErrorf`/`WithLogf`; chromedp's default `errf` prefixes `ERROR: ` and prints through `log.Printf` (chromedp v0.16.0 `browser.go:112-115`), hence `ERROR: could not retrieve document root for <frame id>: context deadline exceeded` at INFO with the id in the message (`target.go:282`).
3. **A WARN on every shutdown.** `Server.Shutdown` logs `Error closing heartbeat push listeners` (`server/internal/app/server.go:4282`) because `heartbeatpush.Server.Close` (`server/internal/heartbeatpush/listener.go:162-179`) joins the errors of `tcp.Close()` / `udp.Close()`, and both listeners are already closed by the context cancellation: `use of closed network connection` (`net.ErrClosed`).
4. **Operator notices over web push.**
   - Duplicate `event` attribute: `OperatorNoticeJob` builds `log := jctx.Logger.With("event", …)` (`server/internal/jobs/jobtypes/job_operator_notice.go:135`) and `DeliverToUser` adds `log.With("recipientUid", …, "event", notice.Event)` again (`server/internal/opsnotify/deliver.go:183`).
   - A gone subscription (`webpush.ErrSubscriptionGone`, 404/410, `server/internal/webpush/sender.go:17,65`) is logged as `<label> failed to send over webpush` at WARN by `classifySend` (`deliver.go:102`) and the contact is kept, so it fails again on every notice. The escalation path already prunes it at INFO (`job_escalation_step.go:723-731`, `DeleteUserContact`).
5. **Startup WARN on every node.** `ReportDanglingProviderLinks` (`server/internal/handlers/auth/provider_links.go:139-159`) is called from server startup (`server/internal/app/server.go:2322`) on every node, checks-only nodes included, so one stale link warns once per node per restart.

## Why a spec

- Fix plus tests span well over ~4 source files (tlsedge, checkbrowser, heartbeatpush, opsnotify, auth/app startup) across several subsystems.
- Item 4 deletes data (a user's web-push contact) on a new path.

## Proposal

1. **tlsedge**: give both `http.Server`s an `ErrorLog` built from a small `io.Writer` adapter that parses `http: TLS handshake error from <addr>: <err>` into `DEBUG "TLS handshake error"` with `remote_addr` and `error` attributes (and `sni` when the error carries the host), and forwards any other line at WARN with the raw text as `error`. Test: feed both line shapes to the adapter and assert level, message and attributes.
2. **checkbrowser**: pass `chromedp.WithErrorf` (and `WithLogf`) to `chromedp.NewContext` in `browserContext`, routing to slog: errors at WARN, message `"chromedp error"` with the formatted text as `error` (no id in the message). Downgrade the known benign `could not retrieve document root` (a page torn down mid-navigation) to DEBUG. Test the adapter function.
3. **heartbeatpush**: in `Server.Close`, drop `net.ErrClosed` from the joined errors (`errors.Is`). Test: start, cancel the context, then `Close()` returns nil; a positive control that a real close error still surfaces if one can be simulated, otherwise assert `Close()` on a running server returns nil too.
4. **opsnotify**: remove `"event"` from the `log.With` in `DeliverToUser` (callers that do not carry it, e.g. `handlers/system/operator_notifications.go:237`, `regionsweep/operator.go:240`, must then add it themselves), or keep it in `DeliverToUser` and drop it from the job's logger; pick one and make every caller consistent. In `sendWebPush`, on `ErrSubscriptionGone`: delete the contact with `deps.DB.DeleteUserContact` (add it to the `Deps` DB interface if missing), log INFO `"web push subscription gone; pruning contact"`, count the outcome as `skipped` (or a new `pruned` label if the metric's label set allows it). Tests: a fake `SendWebPush` returning `ErrSubscriptionGone` → contact deleted, no WARN; a captured log handler shows `event` once.
5. **provider links**: call `ReportDanglingProviderLinks` only on nodes that serve the API (the same condition that gates the HTTP API server in `server/internal/app/server.go`), so checks-only nodes stay quiet. Keep the metric set where it is called. Test at the level of that condition if it is a function; otherwise a short comment is enough and no new test (state it in the commit).

Use `acme` placeholders in every test fixture (hosts like `scanner.acme.com`).

## Closing the issue

This spec closes fclairamb/solidping-business#51. The implementing PR body **must** carry a `Closes fclairamb/solidping-business#51` line. When the spec is archived to `specs/done/`, verify the issue is closed and, if not, close it by hand:

    gh issue close 51 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
