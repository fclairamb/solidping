---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# Customers never learn their private-location agent is out of date

## Problem

Agents older than v0.54 predate the rolling upgrade (spec 2026-11-22-04,
`wiki/features/agent-auto-upgrade.md`): the server never asks them to restart and only logs
`agent predates auto-upgrade: upgrade it by hand` at INFO. Agents installed from the old
`ghcr.io/fclairamb/solidping:latest` snippets do not move on restart either. The only signal is
the amber "Drifted" badge on Organization → Private locations, which customers rarely open.
In prod on 2026-11-26, two customer orgs had agents on 0.52.0 and 0.49.3 with no way to know.

## Proposal

1. **Org event.** When an org agent connects and is either legacy
   (`ClientFrame.PredatesAutoUpgrade()`, `server/internal/agents/protocol.go:226`) or still
   drifted 72 h after its first drifted report, and is not pinned (`VersionPinned()`), record an
   org event `agent.outdated` with the agent name, its version, the server version and whether it
   is legacy. Hook it near `warnOnVersionDrift` (`server/internal/handlers/agentws/handler.go:847`).
   The org's private Atom event feed (#1014) picks it up like any other org event.
2. **Email to org admins**, once per agent and per server version (dedup column on `workers`,
   e.g. `outdated_notified_version`, new migration). Subject: "Your SolidPing agent <name> runs
   <v>, the server runs <v>". Body: the exact fix, for Docker
   (`docker compose pull && docker compose up -d`, then switch the image to
   `<server-host>/solidping:current` with the pull timer from the docs) and for Kubernetes
   (`imagePullPolicy: Always`, image `<server-host>/solidping:current`), plus a link to the
   Private locations page. Use the existing email templates (light and dark,
   `wiki/features/email-dark-mode.md`).
3. **In-app banner** on Organization → Private locations
   (`web/dash0/src/routes/orgs/$org/organization.private-locations.index.tsx`) when any agent is
   outdated: same text and fix command as the email, `data-testid="outdated-agents-banner"`.
4. System agents are excluded (they have no org and are ours to upgrade).
5. Docs: `wiki/features/agent-auto-upgrade.md` (new section), `web/docs/docs/features/private-locations.md`
   ("Keeping agents up to date").

## Tests

- agentws handler test: a legacy agent produces one `agent.outdated` event and one email; a
  second connection with the same versions produces none (dedup).
- A drifted non-legacy agent produces nothing before 72 h and the event after.
- A pinned agent and a system agent produce nothing (negative cases).
- After a server upgrade (new server version), the same agent is notified again.
- dash0 Playwright: banner shown with an outdated agent, absent otherwise.

## To verify

- Where org events are written and how the event feed selects them (commit a901e1bcd, #1014),
  and whether a new event type needs an allowlist entry there or in the audit log.
- How to list an org's admins for an email (existing helper used by other org-wide emails).
- Where "first drifted report" can be stored (a timestamp next to `workers.version`).
