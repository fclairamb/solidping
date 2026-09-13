---
model: sonnet
effort: medium
---

# Closing a support thread leaves the operator staring at the thread they just closed instead of taking them to the next one

## Problem

The support inbox (`/support`, dash0 routes `support.index.tsx` and
`support.$threadUid.tsx`) is worked like any inbox: open a thread, deal with
it, move on. "Deal with it" is very often *not* a reply — a thank-you, a
duplicate, spam, a question already answered elsewhere — and for those the
operator clicks **Close thread** (`actions.close`,
`web/dash0/src/locales/en/support.json:60`).

Today that button only flips the status:

```tsx
// web/dash0/src/routes/support.$threadUid.tsx:181-191
<Button … onClick={() => update.mutate({ status: "closed" })} data-testid="support-close">
  {t("actions.close")}
</Button>
```

`useUpdateSupportThread` (`web/dash0/src/api/support.ts:132-146`) invalidates
the thread and list queries and nothing else. The page re-renders the same
thread with a "Closed" badge and a **Reopen** button, and the operator has to
click *Back to the inbox*, find where they were, and open the next row. With a
queue of twenty messages that is forty extra clicks and twenty scroll-and-find
moments, exactly where an inbox should carry you forward on its own.

Note on naming: the request described the page as `/conversations` and the
button as "Close without a reply". In the current tree the route is `/support`
and the label is "Close thread"; there is no `/conversations` route and no
"Close without a reply" string anywhere in `web/dash0/src`. This spec targets
the existing route and button. Whether the label should be renamed is an open
question below, not an assumption.

## Proposal

When a thread is closed from its detail page, advance to the next thread that
still needs attention. Reopening does **not** advance — reopening is a
deliberate revisit, not queue-clearing.

### "Next" is defined by the inbox, not by the API

The inbox renders three sections, in this order, from one unfiltered list
that the backend already sorts by `last_message_at DESC`
(`server/internal/support/service.go:1051`): **Active**, **Unanswerable**,
**Closed** (`splitThreads`, `support.index.tsx:180-197`). The operator's mental
"next" is the row under the one they just dealt with, in that same order, and
closed rows are never a destination. So:

1. `queue` = `active ++ expired` from `splitThreads(threads)` over the
   unfiltered list — the same `useSupportThreads({})` query the inbox issues
   with its default filters, so the cache is shared and no new request is made
   on a normal open-from-inbox visit. (Normalize the filter object so the
   inbox's `{channel: "", status: "", q: ""}` and the detail page's `{}` hit
   the same query key; today they would be two cache entries for the same
   response.)
2. `i` = index of the current thread in `queue`.
3. target = `queue[i + 1]`, else `queue[i - 1]`, else **none**. Next-after
   first, previous as the fallback when the operator was on the last row, the
   way mail clients advance after archiving.
4. Compute the target **at click time**, before `mutate`, from the list as it
   was. After the mutation succeeds the list is invalidated and the closed
   thread migrates to the Closed section, which would shift every index; the
   snapshot avoids depending on refetch timing.
5. In the mutation's `onSuccess`: `navigate({ to: "/support/$threadUid",
   params: { threadUid: target.uid }, replace: true })` when there is a
   target, otherwise `navigate({ to: "/support", replace: true })`.
   `replace: true` so the browser Back button returns to the inbox, not to a
   thread the operator has already closed.
6. If the list query has not resolved yet when the button is clicked (cold
   deep link, list still loading) there is no queue to walk: close as today
   and fall back to `/support`. Do not block the button on the list.

The thread that was closed should be acknowledged in passing, since the
operator no longer sees its "Closed" badge: a short `toast.success` using a
new `actions.closed` key ("Thread closed"), mirroring the existing
`reply.sent` toast. Add the key to all four locales (`en`, `fr`, `de`, `es`;
`locale-parity.test.ts` enforces it).

### Files

- `web/dash0/src/routes/support.$threadUid.tsx` — `ThreadHeader`: add
  `useNavigate`, `useSupportThreads({})`, the queue/target computation and the
  `onSuccess` navigation. Keep the Reopen branch untouched.
- `web/dash0/src/api/support.ts` — normalize `SupportThreadFilters` in
  `useSupportThreads`'s query key so empty and absent filters share a cache
  entry. Optionally export a small pure `nextThreadAfterClose(threads,
  currentUid)` helper so the ordering rule has a unit test.
- `web/dash0/src/locales/{en,fr,de,es}/support.json` — `actions.closed`.
- `web/docs/docs/features/support-inbox.md` — one sentence: closing advances
  to the next thread.
- `CHANGELOG.md` — a `feat(support)` entry.

### Tests

- **Unit** (`bun run test:unit`): `nextThreadAfterClose` — middle of the
  queue advances forward; last row falls back to previous; single row yields
  none; closed rows are skipped as candidates; an active row is preferred over
  an unanswerable one only through list order, never by section priority.
- **E2E** (`web/dash0/e2e/support-inbox.spec.ts`, stubbed with the existing
  `stubSupportApi` and `page.route` pattern at lines 141-190):
  - three answerable threads; open the first; click `support-close`; assert
    the PATCH body is `{"status":"closed"}` and the URL becomes the second
    thread's, whose `support-thread-status` reads "Open".
  - open the last thread; close; URL becomes the previous thread's.
  - a single thread; close; URL is `/support` and the inbox shows it under
    the Closed section.
  - on a closed thread, click `support-reopen`; the URL does not change.
  - `page.goBack()` after an auto-advance lands on `/support`, not on the
    closed thread.

### Open questions

- **Rename the button?** "Close without a reply" says what the action is for
  more precisely than "Close thread", and reads well next to *Send reply*.
  If wanted, it is a locale-only change (`actions.close` in four files) and can
  ride along; this spec does not assume it.
- **Respect the inbox's filters?** The inbox's channel/status/search filters
  live in component state (`support.index.tsx:56-58`), not the URL, so the
  detail page cannot see them and the queue is the *unfiltered* inbox order.
  If an operator working a filtered view finds the auto-advance jumping to a
  thread outside their filter, the fix is to lift the filters into the route's
  search params first (see `wiki/` notes on dash0 URL search state), which is
  its own spec.
