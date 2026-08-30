---
model: sonnet
effort: medium
---

# WhatsApp messages sent to SolidPing are never marked as read (no blue checks)

## Problem

On the WhatsApp Cloud API, read receipts are never automatic for a business
number: the sender only sees the double blue check when the business explicitly
calls Meta's mark-as-read API for an inbound message:

```
POST /{phone_number_id}/messages
{ "messaging_product": "whatsapp", "status": "read", "message_id": "wamid.…" }
```

SolidPing never makes that call. The webhook
(`server/internal/handlers/whatsappcb/handler.go`, `captureInbound`) records
every inbound message into the support inbox via `CaptureSafe`, and the client
(`server/internal/integrations/whatsapp/client.go`) only knows how to send
templates and free-form texts. So from the sender's phone, a message to
SolidPing stays at "delivered" (two grey checks) forever — even after an
operator has read the thread and answered it. Combined with an actual reply
this looks actively broken: "they answered me but never read my message?"

### Semantics decision (already made)

The read receipt should mean **"the intended reader read it"**:

- Support messages are addressed to a human → mark read **when an operator
  actually reads the thread** (or replies to it), not when the server receives
  the webhook.
- A future WhatsApp *command* path (none exists today — inbound replies carry
  no commands in v1) is addressed to the machine → the command dispatcher
  should mark read **immediately on receipt**. That path is explicitly out of
  scope here, but the mechanism built now must let it drop in: policy in the
  callers, mechanism in the client.

## Proposal

### 1. `MarkRead` on the WhatsApp client

Add `func (c *Client) MarkRead(ctx context.Context, messageID string) error`
to `server/internal/integrations/whatsapp/client.go`, posting the payload above
to the same `/{phone_number_id}/messages` endpoint.

- The success response is `{"success": true}` — **not** a `messages[]` array —
  so it cannot reuse `postMessage` as-is (that would return `ErrNoMessageID`).
  Either factor the request/error-classification plumbing out of `postMessage`
  or add a small dedicated post; non-2xx responses go through the existing
  `classifyError` so failures stay typed.
- Document on the method that Meta's read receipts are **cumulative**: marking
  one message read blue-checks every earlier message in the conversation too.
  This matters later — a command's immediate receipt will also blue-check an
  older, still-unread support question in the same conversation. That is Meta
  behavior we cannot opt out of; the comment exists so nobody files it as a
  bug.

### 2. A per-channel read-receipt hook on the support service

`internal/support` is a leaf package (see the import-cycle note at the top of
`server/internal/app/support_wiring.go`), so it cannot import the whatsapp
integration. Mirror the replier registration pattern:

- `type ReadReceiptFunc func(ctx context.Context, thread *models.SupportThread, externalID string) error`
- `func (s *Service) RegisterReadReceipt(channel string, fn ReadReceiptFunc)`
- Wire it in `registerSupportRepliers` (`server/internal/app/support_wiring.go`),
  guarded by `s.config.WhatsApp.Active()`, calling `client.MarkRead`. Only
  WhatsApp registers one in v1; channels without a hook are a silent no-op.

The hook targets the **newest inbound message on the thread that carries an
`ExternalID`** (the `wamid.…` is already persisted there by `captureInbound` —
no schema change). Cumulativity makes marking the newest one sufficient. Add a
small service-side lookup for it (the service already has
`findMessageByExternalID`; this is the inverse: newest inbound external id for
a thread).

### 3. Trigger A — operator opens the thread

`GetThread` (`server/internal/handlers/supportinbox/handler.go:186`) already
calls `svc.MarkRead` when `thread.UnreadCount > 0`. Fire the channel's
read-receipt hook on that same transition. Preferably inside
`Service.MarkRead` (`server/internal/support/service.go:1086`) rather than the
handler, so any caller that zeroes the counter gets the receipt — but only
when the counter was actually `> 0` before zeroing. That gate is the
idempotency: re-opening an already-read thread must not re-call Meta.
(`MarkRead` currently takes only the uid; the handler has the thread loaded —
pass what's needed rather than re-querying per open.)

### 4. Trigger B — operator replies

Replying does not implicitly mark read on WhatsApp, and a reply sent through
the API or an automation may never hit the `GetThread` hook. In
`Service.Reply` (`server/internal/support/service.go:648`), after a
*successful* send, fire the same hook when the thread still has
`UnreadCount > 0` (same idempotency gate — if the open-thread hook already
fired, this is a no-op).

### 5. Best-effort, everywhere

A failed read receipt is logged (warn, with thread uid and message id) and
dropped. It must never:

- fail or delay the inbox request (`GetThread`),
- turn a successful reply into an error,
- retry in a loop.

Meta may reject receipts for old messages; that is a logged no-op, not a bug.

### Non-goals

- No mark-read on webhook receipt (that is the future command path's job, and
  it will call `client.MarkRead` directly from its dispatcher).
- No `typing_indicator` support (same endpoint accepts one; separate feature).
- No read receipts for Telegram or any other channel.
- No new config: gated on the existing `WhatsApp.Active()`.

### Testing

- **Client**: httptest fake asserting the exact wire payload
  (`messaging_product`/`status`/`message_id`), the `{"success":true}` happy
  path, and that a Graph error classifies through the existing typed sentinels
  (e.g. 401 → `ErrTokenExpired`).
- **Service**: receipt fires exactly once per unread→read transition; targets
  the newest inbound `ExternalID`; does not fire when `UnreadCount` is already
  0, when the thread has no inbound `ExternalID`, or when no hook is
  registered for the channel; a hook error leaves `MarkRead` and `Reply`
  outcomes unchanged.
- **Reply path**: successful reply with unread messages fires the receipt;
  failed provider send does not.
