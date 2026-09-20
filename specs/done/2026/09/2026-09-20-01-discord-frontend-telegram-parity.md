---
model: sonnet
effort: medium
---

# The dashboard still branches on `telegram` in places that should also cover `discord`

## Problem

The `discord` user-contact type shipped with spec
`specs/done/2026/09/2026-09-19-05-discord-parity-dm-contact-mentions-e2e.md`, but two
frontend sites still enumerate `telegram` by hand and were never extended.

### 1. `canTest` — an unverified Discord route offers a Test button that cannot work

[`web/dash0/src/routes/orgs/$org/account.notifications.tsx:432`](web/dash0/src/routes/orgs/$org/account.notifications.tsx:432):

```ts
const canTest = (!needsVerification && !isTelegram) || isVerified;
```

`isTelegram` is the only "connected, not code-verified" type the rule knows about
(line 425). The backend's `contactRequiresSetup`
([`server/internal/handlers/usernotifications/service.go:738`](server/internal/handlers/usernotifications/service.go:738))
now treats `discord` exactly like `telegram`: connected or not, no verification code.
So an *unverified* discord route renders a Test button whose call answers
`ErrContactNotVerified`.

This is practically unreachable today. Nothing clears a discord contact's verification —
`ClearUserContactVerified` is only called from the Telegram escalation path and member
provisioning — and `job_escalation_step_discord_test.go:292` explicitly pins that a
Discord `50007` does *not* unverify the contact. This is a consistency fix, not a live
bug: the frontend rule and the backend readiness rule must agree, and the comment above
`canTest` already claims they do.

### 2. `getCommentSource` drops Discord — a live gap, not a theoretical one

The grep sweep for other `isTelegram` / `"telegram"` branches found a second omission,
and this one *does* reach users.

[`web/dash0/src/components/dashboard/event-display.tsx:348-357`](web/dash0/src/components/dashboard/event-display.tsx:348):

```ts
export function getCommentSource(event): "web" | "slack" | "telegram" | undefined {
  const source = event.payload?.source;
  if (source === "slack") return "slack";
  if (source === "telegram") return "telegram";
  if (source === "web") return "web";
  return undefined;
}
```

The backend writes `source: "discord"` on comments ingested from a Discord thread
(`CommentSourceDiscord`, [`server/internal/handlers/incidents/service.go:58`](server/internal/handlers/incidents/service.go:58),
written at lines 2754 and 2773). `getCommentSource` maps that to `undefined`, so the
incident timeline
([`web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx:690-700`](web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx:690))
renders a Discord-ingested comment with **no source badge at all**, where a Slack or
Telegram one gets "via Slack" / "via Telegram". There is no `comments.viaDiscord` key in
`web/dash0/src/locales/*/incidents.json`.

Sites checked and found **already correct** (no change needed): `ACK_CHANNEL_KEYS` and
`ACK_ACTOR_PAYLOAD_KEYS` both list discord; `integration-icon.tsx` has a discord case;
`account.notifications.tsx:1160` already has a `discordConnected` twin of
`telegramConnected`.

## Proposal

### Fix 1 — extract the readiness rule and cover discord

Rather than adding a second ad-hoc boolean inline, move the rule to a pure helper in
[`web/dash0/src/lib/notifications.ts`](web/dash0/src/lib/notifications.ts) so it can be
unit-tested without mounting the route:

```ts
/** Contact types with a setup round-trip: code verification for phone/WhatsApp,
 * pressing Start / joining the DM for Telegram and Discord. Mirrors the backend's
 * contactRequiresSetup (server/internal/handlers/usernotifications/service.go). */
export function contactCanTest(type: string, verifiedAt?: string | null): boolean
```

Keep the existing comment's intent: any *future* type gets the Test button by default,
only the enumerated setup types gate on `verifiedAt`. Call it from
`account.notifications.tsx:432` in place of the inline expression; keep `isTelegram`
where it is still genuinely Telegram-specific (the connected/reconnect badges and
`TelegramConnect` at lines 446, 452, 461, 512 are correct as-is — Discord has its own
equivalents).

Unit test it in [`web/dash0/src/lib/notifications.test.ts`](web/dash0/src/lib/notifications.test.ts):

- unverified `discord` → **false** (the fix)
- verified `discord` → **true** (positive control: Test still shows)
- the same pair for `telegram`, `phone`, `whatsapp` (no regression)
- `email` / `webpush` / an unknown future type → **true** regardless of `verifiedAt`

### Fix 2 — teach `getCommentSource` about Discord

Add `if (source === "discord") return "discord";` and widen the return type, then render
the badge in `incidents.$incidentUid.tsx` alongside the Slack and Telegram ones. Add
`comments.viaDiscord` to **all four** of
`web/dash0/src/locales/{de,en,es,fr}/incidents.json` — `locale-parity.test.ts` fails
otherwise. Extend `event-display`'s existing unit test (or add one) to pin the new
mapping.

### Optionally

`web/dash0/e2e/account-notifications-discord.spec.ts` already exists; extending it is
welcome but not required — the unit tests above are the primary pin, and an unverified
discord contact is not reachable through the UI without fixture surgery.

### Gate

- `make build-dash0`
- `cd web/dash0 && bun run lint` — no **new** errors; the pre-existing react-hooks errors
  are known debt, do not fix them
- `cd web/dash0 && bun run test:unit`
