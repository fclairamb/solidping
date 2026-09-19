---
model: sonnet
effort: medium
---

# The "Incidents" card on a check's detail page shows a count but goes nowhere

## Problem

On the check detail page (`/orgs/$org/checks/$checkUid`), the summary row shows
three cards: uptime/downtime, last checked, and **Incidents** with a count
([`check-summary-cards.tsx:70-79`](web/dash0/src/components/checks/check-summary-cards.tsx:70)).

The Incidents card is a dead end. It reports "2" and offers no way to see which
two. To actually look at them the user has to navigate to the incidents page by
hand and then re-pick the check in the check filter — even though the page
already supports exactly that filter: `/orgs/$org/incidents` accepts a
`checkUid` search param that takes a uid *or* a slug
([`incidents.index.tsx:63-72`](web/dash0/src/routes/orgs/$org/incidents.index.tsx:63)).

The plumbing exists on both ends; only the link is missing.

## Proposal

Make the Incidents card a link to the incidents list, pre-filtered to this check.

1. **`check-summary-cards.tsx`** — wrap the Incidents `Card` in a TanStack
   `Link` to `/orgs/$org/incidents` with
   `search={{ checkUid: <check uid>, state: "all" }}`.
   - `state: "all"` matches what the page already defaults to, so the landing
     view shows every incident the card counted, not just the active ones.
     The card's count comes from `useIncidents(org, { checkUid, size: 100 })`
     with no state filter ([`checks.$checkUid.index.tsx:934`](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:934)),
     so the destination must not narrow it — the number the user clicked and
     the number of rows they land on must agree.
   - Pass the **uid**, not the slug: the incidents query does not resolve a slug
     filter (issue #127, see the comment at
     [`checks.$checkUid.index.tsx:930`](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:930)).
     The page-level `checkUid` search param tolerates both, but only the uid is
     guaranteed to actually filter.
2. **Affordance** — the card must read as clickable, not just be clickable:
   hover state (`hover:bg-accent/50` or the equivalent already used elsewhere),
   `cursor-pointer`, and focus-visible ring. Follow whatever clickable-card
   pattern the design reference at `/d/orgs/default/design-reference` already
   ships; if it ships none, add one there as part of this change (mandatory per
   `CLAUDE.md`'s frontend rules).
3. **Zero incidents** — when `totalIncidents === 0` there is nothing to show.
   Keep the card non-interactive in that case (no link, no hover affordance)
   rather than linking to an empty list.
4. Keep `data-testid="incidents-card"` on the same element so existing
   selectors keep working.

### Tests

- Playwright (`web/dash0/e2e/`): on a check with at least one incident, click
  the incidents card and assert the URL is `/orgs/$org/incidents` carrying
  `checkUid=<uid>`, and that the rendered rows are scoped to that check.
- Positive control for the zero case: a check with no incidents renders the card
  without a link (asserting it is *not* an anchor, so a regression that always
  links can't pass).

### Open question

Should the count (and therefore the link) be scoped to *active* incidents rather
than all of them? Today it is all-time, capped at `size: 100`. This spec keeps
the existing semantics and only adds the link — changing what the number means
is a separate decision.
