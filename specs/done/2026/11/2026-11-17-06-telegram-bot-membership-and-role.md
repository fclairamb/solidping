---
model: opus
effort: high
---

# The Telegram bot keeps acting for removed members and lets viewers acknowledge and comment

## Problem

Found by the security scan of 2026-11-17 (finding AP1). Severity: Medium. Two kinds of people can trigger it: a former member of the org who linked a Telegram chat while still a member, and a `viewer`-role member who linked a chat (any member can link one: `POST /orgs/:org/users/me/telegram/link` sits on `orgGroupSelf`, `server/internal/app/server.go:1741-1752`).

- **The bot never re-checks membership.** A chat's orgs come only from its verified Telegram contacts:
  - `linkedContacts` keeps every contact with `VerifiedAt != nil` (`server/internal/handlers/telegramcb/commands.go:24-40`);
  - `ListUserContactsByTypeValue` matches on type and value across every user and org, with no membership join (`server/internal/db/postgres/user_contact.go:320-337`, same in sqlite);
  - `linkedOrgs` / `requireLinkedOrgs` turn those contacts into the orgs a command may act in (`server/internal/handlers/telegramcb/resolver.go:31-71`).
- **Removing a member leaves the contact live.** `RemoveMember` soft-deletes the membership row and nothing else (`server/internal/handlers/members/service.go:471-528`). The other membership deleters (`handlers/auth/org_delete.go:285`, `jobs/jobtypes/job_demo_cleanup.go:249`) do not touch contacts either. A contact only goes away on `/stop`, on a bot block, or after a delivery failure.
- **Writes have no role check.** `/ack`, the inline ack button and `/comment` call `AcknowledgeIncidentFromTelegram` / `AddCommentFromTelegram` (`server/internal/handlers/incidents/service.go:3225-3234`, `:3393`; callers `telegramcb/commands.go:185-210`, `telegramcb/callback.go:18-117`, `telegramcb/comment.go:22`). Neither checks the member's role. The web twins (`POST /incidents/:uid/acknowledge`, `/comments`) sit on `orgGroup`, whose write floor refuses viewers (`server/internal/app/server.go:941-947`, `:1629-1633`).
- **Impact.**
  - A removed member's API sessions stop working at once (membership is checked live in `RequireOrgAccess`), but their chat keeps answering `/status`, `/incidents` and `/incident <ref>` with live org health and incident detail, with no time limit.
  - The same chat can acknowledge incidents (which stops the escalation paging) and post attributed timeline comments in an org the person no longer belongs to.
  - A viewer, documented as read-only, can acknowledge and comment through the bot.
- The webhook secret token authenticates Telegram, not the SolidPing member, so it does not close this.
- Related but distinct: M7 (removed members keep MCP access) is the MCP-token side of the same offboarding gap.

## Proposal

1. **Guard in the bot (the fix that holds even if a cleanup is missed).** In `linkedOrgs` (`telegramcb/resolver.go:31`), drop every contact whose `(UserUID, OrganizationUID)` has no live, joined membership (`GetMemberByUserAndOrg`, `deleted_at IS NULL`, `joined_at IS NOT NULL`). Also drop it when the user is deleted. Keep the member's role on `linkedOrg`.
2. **Role floor on writes.** In `handleAck`, the ack callback and `handleComment`, refuse the action when the linked member's role is below `user`. Answer with a short "your role in <org> is read-only" message, and leave read commands available to viewers. Better still, enforce the same floor inside `AcknowledgeIncidentFromTelegram` / `AddCommentFromTelegram`, so a future caller cannot skip it.
3. **Clean up on removal.** When a membership ends (`members.RemoveMember`, `auth/org_delete.go`, the demo cleanup job), delete that user's contacts and notification routes scoped to that org, in the same transaction as the membership delete if possible. Record it in the audit log entry of the removal.
4. If a contact is dropped by the guard of step 1, the bot answers as for an unlinked chat (`BuildNotLinkedHTML`), so a removed member learns nothing about the org.

## Tests

- `server/internal/handlers/telegramcb`: a chat linked in org A, whose membership is then removed. `/status`, `/incidents`, `/incident #1`, `/ack #1`, the ack button and `/comment #1 x` all get the not-linked answer. The incident stays unacknowledged and gets no comment. This fails before the fix. Positive control: the same flow before the removal acks the incident and posts the comment.
- `telegramcb`: a viewer's linked chat. `/status` and `/incidents` answer normally (positive control). `/ack`, the ack button and `/comment` are refused and change nothing. This fails before the fix. A `user`-role chat can ack (positive control).
- `server/internal/handlers/members`: `RemoveMember` deletes the removed user's contacts and routes in that org, and keeps their contacts in another org they still belong to (positive control).
- `incidents` service: `AcknowledgeIncidentFromTelegram` with a viewer or a non-member user UID returns an error and leaves the incident unacknowledged.

## To verify

- Whether account deletion (the user deleting their own account) soft-deletes `user_contacts`. If not, step 1's user check covers it, and the cleanup should be added there too.
- Whether the Slack and other chat-ops integrations map their actors to SolidPing members with the same gap. If one does, list it in the PR rather than widening this spec.
