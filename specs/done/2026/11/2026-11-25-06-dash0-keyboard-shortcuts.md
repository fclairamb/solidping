---
model: sonnet
effort: medium
pr: own
automerge: false
---

# dash0 has no page-level keyboard shortcuts

## Problem
dash0 has Cmd/Ctrl+K for the command menu (`web/dash0/src/components/CommandMenu.tsx:202`) and Enter/Escape inside a few inputs, nothing at page level. On the checks list, filtering on a group (`/d/orgs/default/checks?group=region-heartbeats`) can only be undone by clicking "Reset" (`resetFilters()`, `web/dash0/src/routes/orgs/$org/checks.index.tsx:1263`, button `data-testid="reset-filters"` at `:1986`). Esc should do it, and a few other common actions deserve a key.

## Proposal
1. Add a `useHotkey` hook (e.g. `web/dash0/src/hooks/use-hotkey.ts`), no new dependency. It:
   - registers on `document` `keydown`, cleaned up on unmount;
   - ignores events with `e.defaultPrevented` (Radix dialogs, popovers, dropdowns and the Cmd+K menu handle their own Esc first);
   - ignores events whose target is an `input`, `textarea`, `select` or `contenteditable`, unless the shortcut opts in (used for Cmd+Enter and the search-box Esc below);
   - ignores events with Ctrl/Cmd/Alt held, unless the shortcut is declared with that modifier.
2. Layered Esc on the checks list (`checks.index.tsx`), innermost first:
   1. an open Radix layer or the Cmd+K menu closes (nothing else happens: the event arrives `defaultPrevented` or never reaches the page handler);
   2. focus in the search box: first Esc clears its text (the `q` param, `:1234`), the next one blurs it;
   3. otherwise, when `hasActiveFacets` (`:1257`), call `resetFilters()`. Search is kept, exactly like the Reset button (spec 2026-09-30-02).
3. `/` focuses the search box on list pages: checks (`checks.index.tsx`), incidents (`incidents.index.tsx`), events (`events.tsx`). `preventDefault` so `/` is not typed into it.
4. Cmd/Ctrl+Enter submits the form on the dedicated edit routes: `checks.$checkUid.edit.tsx`, `checks.new.tsx`, `integrations.$integrationUid.tsx`, `escalation-policies.$uid.tsx`, `escalation-policies.new.tsx`. Same path as the Save button (validation included); does nothing while a save is pending.
5. `n` on the checks list navigates to `checks.new`.
6. `?` (Shift+/) opens a small dialog listing the shortcuts (`data-testid="shortcuts-help"`), mounted once in the org layout. Entries are i18n strings like the rest of dash0.
7. Show a `kbd`-style `Esc` hint next to the Reset button (hidden on touch/mobile widths).
8. Add the hook usage and the shortcuts dialog to the design reference page (`web/dash0/src/routes/orgs/$org/design-reference.tsx`). If there is no `Kbd` primitive in `components/ui/`, add one there and show it on that page.

Out of scope: g-prefixed navigation (Cmd+K covers it), j/k list navigation, single-key actions with side effects (e.g. acknowledging an incident).

## Tests
- `web/dash0/e2e/keyboard-shortcuts.spec.ts`:
  - Esc on `/checks?group=<slug>` clears `group` from the URL and the Reset button disappears; `q` is kept.
  - Esc with status + labels facets set clears both.
  - Layering: open a filter dropdown, press Esc: the dropdown closes and the filters (URL) are unchanged.
  - Search box: type text, Esc clears it, second Esc blurs, third Esc resets facets.
  - Typing guard: with focus in an unrelated input, `n` and `/` are typed into it, no navigation.
  - `/` focuses the search box on checks, incidents and events.
  - `n` opens the new-check page.
  - Cmd/Ctrl+Enter on a check edit page saves (positive) and does not save when the form is invalid (negative).
  - `?` opens the shortcuts dialog, Esc closes it without touching filters.
- Unit test for `useHotkey` (vitest if dash0 has unit tests for hooks): modifier and editable-target filtering, `defaultPrevented` ignored.

## To verify
- Whether incidents and events lists have a search box and facet filters; if they have facet filters with a reset action, apply the same layered Esc there.
- Whether the sidebar component (`components/ui/sidebar.tsx`) already binds a key that conflicts.
- Where dash0 keeps hooks (`src/hooks/` or `src/lib/`).
