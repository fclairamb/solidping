---
model: sonnet
effort: high
---

# Telegram alerts bury "View incident" in a text link instead of offering it as a button

## Problem

Telegram incident alerts already carry an inline keyboard, but it holds a single
button — ✅ Acknowledge (`AckKeyboard`,
`server/internal/integrations/telegram/incidentview.go:56`). The dashboard deep
link is rendered as a trailing `<a href>` text line inside the message body
("View incident →", `server/internal/integrations/telegram/message.go:130-136`),
which is easy to miss, small to tap on mobile, and inconsistent with the
Acknowledge action sitting right below it as a proper button.

Telegram's `inline_keyboard` natively supports multiple buttons per row and
multiple rows, and the repo already models that
(`InlineKeyboard [][]InlineButton`,
`server/internal/integrations/telegram/client.go:222`), with `InlineButton`
supporting `url` buttons alongside `callback_data`
(`client.go:208`). Only the helpers are single-button.

Secondary annoyances in the same area:

- Once an incident is acked or resolved, the code removes the *whole* keyboard
  (`EmptyInlineKeyboard()` in
  `server/internal/handlers/telegramcb/callback.go:163-166` and
  `server/internal/jobs/jobtypes/job_escalation_step_telegram.go:255-259`).
  With a View button in play, that edit would also strip the navigation
  affordance — the edit should drop only the Ack button and keep View.
- The dashboard incident URL is built in two independent places with the same
  string concatenation (`job_escalation_step_telegram.go:508` and
  `server/internal/handlers/telegramcb/commands.go:480`).
- The `/incidents` listing and `/incident <#ref>` detail replies
  (`commands.go:~150`) attach only the Ack button (or none when acked), while
  each line already computes an `IncidentURL` — same missed opportunity.

## Proposal

1. **Multi-button keyboard helpers** in
   `server/internal/integrations/telegram/`:
   - Keep `NewInlineKeyboard` (single row, variadic) and add whatever tiny
     helper the call sites need for a two-button row — no speculative
     multi-row API.
   - New `IncidentKeyboard(incidentUID, incidentURL string, canAck bool) *InlineKeyboard`
     (name flexible) producing one row:
     `[✅ Acknowledge][🔎 View]` when `canAck`, `[🔎 View]` when not, `nil`
     when both are absent. A URL button needs no callback verb, no
     `ParseCallbackData` change, no dispatch change — `callback_data` stays
     ack-only.
2. **Move the link from body to button** on the escalation alert path
   (`sendTelegramAlertTo`, `job_escalation_step_telegram.go`):
   - Attach the View URL button to every alert (open, acked, resolved) —
     unlike Ack, a URL button is never stale noise; viewing a resolved
     incident's history is legitimate.
   - Drop the trailing "View incident →" anchor line from `BuildAlertHTML`
     when the URL ships as a button, so the message doesn't say it twice.
     (Careful: `AlertParams.IncidentURL` is also consumed by other views —
     check `incidentview.go:190` and `:262` before removing anything shared.)
3. **Edits preserve the View button**: the two `EmptyInlineKeyboard()` edit
   sites (ack callback, resolution rewrite of the original message) switch to
   the View-only keyboard instead of removing everything. The pinned test
   `TestEmptyInlineKeyboardSerializesAsRemoval` and the "absent reply_markup
   leaves buttons in place" semantics documented in `client.go:341-345` stay
   valid — the marker is still needed wherever no URL is available.
4. **One URL builder**: dedupe `telegramIncidentURL`
   (`job_escalation_step_telegram.go:508`) and the copy in
   `commands.go:480` into a single function in the `telegram` package (it
   already owns the incident-view rendering).
5. **`/incidents` + `/incident` replies** use the same combined keyboard, so
   every incident line is tappable through to the dashboard.
6. **Tests**: extend the existing pinned tests
   (`client_test.go` keyboard-carrying tests, `incidentview_test.go`
   callback-data tests, webhook/callback tests) to cover: two-button row
   ordering, View-only keyboard after ack/resolve edits, URL button carrying
   no `callback_data`, and the 64-byte `callback_data` cap still holding.

Out of scope: new callback actions (resolve/mute/snooze buttons) — they need
verb + dispatch + permission work and deserve their own spec if wanted.
