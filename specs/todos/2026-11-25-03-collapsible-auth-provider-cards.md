---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# Server Settings > Authentication is one long wall of provider forms

GitHub Issue: https://github.com/fclairamb/solidping/issues/1026

## Source

Jared-Is-Coding finds `/d/orgs/default/server/auth` lengthy and clunky, and asks for each
card (Local login, Google, …) to be collapsible and collapsed on page load. Florent agreed and
suggested treating authentication mechanisms as modules one explicitly adds; Jared answered
that keeping everything available is fine, it just needs to be more compact.

## Problem

`AuthSettingsPage` (`web/dash0/src/routes/orgs/$org/server.auth.tsx:289`) renders the local
login card (`:501`) and then, for every provider of the static list, a full `Card` whose
`CardContent` always shows every switch and field (`:523` onwards): SAML SP info, the Discord
setup panel, every client id/secret, claims and group mappings. With nine providers the page
is several screens long, although an instance typically uses zero to two of them.

## Why a spec

- A product/UX decision the reporter did not settle: collapsible cards (reporter) versus
  "add a mechanism" modules (Florent's comment).

## Proposal

1. Each provider card becomes collapsible. The header (title, description, a status badge:
   *Enabled*, *Configured*, *Not configured*, and the Enabled switch) stays visible; the
   chevron toggles `CardContent`. Reuse `web/dash0/src/components/ui/collapsible.tsx` (or
   `collapsible-section.tsx` if it fits).
2. Collapsed by default, except a provider that is enabled, or configured, so the active
   setup is visible at a glance. A card with unsaved edits stays open.
3. `data-testid`: `provider-card-toggle-<name>`, `provider-card-status-<name>`. Existing
   test ids keep working once the card is expanded; update the Playwright specs that fill
   provider fields to expand the card first.
4. i18n: the status labels and the toggle's aria-label in the five locales.
5. Tests: a Vitest or Playwright check that the page loads with unconfigured cards collapsed,
   a configured one expanded, and that expanding shows the fields.

The local-login switch behaviour (disabled while no SSO provider is live) belongs to spec
2026-11-25-02.

## Open questions

1. Collapsible cards, or an "Add authentication method" picker that only lists the methods
   in use? **Recommended: collapsible cards** (the reporter's ask, and it keeps every method
   discoverable). Trade-off: the picker is more compact still, but hides what is available
   and needs a notion of "added but not configured" that has no backing state today.

## Closing the issue

This spec closes #1026. The implementing PR body **must** carry a `Closes #1026` line so the
squash-merge closes it. When the spec is archived to `specs/done/`, verify the issue is
closed and, if the merge did not close it, close it by hand:

    gh issue close 1026 --comment "Implemented by <PR or commit>; spec: <archived spec path>"
