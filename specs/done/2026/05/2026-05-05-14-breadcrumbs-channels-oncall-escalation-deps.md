# Breadcrumbs for channels, on-call, escalation-policies, dependencies

## Context

`Breadcrumbs` in `web/dash0/src/routes/orgs/$org.tsx` (L79–319) covers
the older top-level sections — dashboard, checks, incidents, events,
status-pages, badges, test, account, organization, server. It silently
returns `null` for the four newer sections that have since landed in
the sidebar (`web/dash0/src/components/layout/AppSidebar.tsx` L48–99):

| titleKey            | path                                  | icon         |
|---------------------|---------------------------------------|--------------|
| dependencies        | `/orgs/$org/dependencies`             | GitBranch    |
| onCall              | `/orgs/$org/on-call`                  | CalendarClock|
| escalationPolicies  | `/orgs/$org/escalation-policies`      | ArrowUpRight |
| channels            | `/orgs/$org/channels`                 | Bell         |

So when you click any of those in the sidebar, the breadcrumb area
above the page just goes blank — there's no anchor, no path back, no
visual continuity between sidebar click and page header. Every other
section has it. This spec adds the missing four branches.

All translation keys already exist in `web/dash0/src/locales/{en,fr}/nav.json`:
`dependencies`, `onCall`, `escalationPolicies`, `channels`. No locale
edits needed.

## Honest opinion

1. **One mechanical sweep, one PR.** Same pattern as spec
   `2026-05-03-56-header-icon-and-breadcrumb-consistency.md` (which
   added `badges` + `test`). Don't extract a helper for the four —
   each branch is a few lines of JSX and the existing branches are
   inlined the same way; adding a helper now would just blur the
   diff.
2. **Reuse the existing `linkClass` / `activeClass` / `iconClass`**
   constants at the top of `$org.tsx` — they already encode the
   muted-foreground + flex pattern.
3. **Channels detail breadcrumb shows the channel `name`**, not the
   `type`. Same convention as status pages (`statusPage.name`),
   incidents (`incident.checkName`), checks (`check.name`). The type
   icon is already in the sidebar entry; the breadcrumb's job is to
   identify *which* channel.
4. **On-call and escalation policies are slug-routed, not UID-routed.**
   The breadcrumb fetch hooks need to take the slug and resolve to a
   name. Use the existing `useOnCallSchedule(org, slug)` and
   `useEscalationPolicy(org, slug)` hooks — they're already cached by
   the detail pages, so the breadcrumb fetch is a cache hit on
   navigation.
5. **Dependencies has no detail route today** (the index page is the
   whole feature). One simple branch — section name + icon, no
   sub-segments. If a detail route is added later, extend.
6. **Don't touch the page h1s in this spec.** Each of these four
   pages already has an `h1` with the icon (verified by reading
   `dependencies.index.tsx` L77–80, `on-call.index.tsx` L41–43,
   `escalation-policies.index.tsx` L42–48, channels uses a smaller
   `text-2xl` header). Header consistency is a separate paper cut.

## Scope

In scope (one PR):

- Add four branches to `Breadcrumbs` in `$org.tsx` for the sections
  above, mirroring the existing pattern.
- For channels: branches for the index, `new`, and `$connectionUid`
  (detail). The detail branch resolves the channel name via a new
  `useConnection(org, uid)` call — already exists in `api/hooks.ts`.
- For on-call: branches for index, `new`, and `$slug` (detail). Use
  `useOnCallSchedule(org, slug)` for the name.
- For escalation-policies: branches for index, `new`, and `$slug`
  (detail). Use `useEscalationPolicy(org, slug)` for the name.
- For dependencies: single branch (index only).

Out of scope:

- Page-h1 visual sweep (those headers are fine; consistency is a
  separate concern).
- Slug auto-derivation from name on the new pages (separate paper cut).
- Default field values on the new pages (channels handled in spec 15;
  the others are deferred — see spec 15's "Honest opinion" for the
  reasoning).
- Edit-as-separate-page for channels (today the detail page IS the
  edit page).

## Implementation

### `routes/orgs/$org.tsx`

Imports:

```tsx
import {
  /* … existing icons … */
  Bell,           // channels
  CalendarClock,  // on-call
  ArrowUpRight,   // escalation-policies
  GitBranch,      // dependencies
} from "lucide-react";

import { useConnection, useOnCallSchedule, useEscalationPolicy } from "@/api/hooks";
```

Add detection flags alongside the existing ones (around L86–91):

```tsx
const isChannels = matches.some((m) => m.routeId.startsWith("/orgs/$org/channels"));
const isOnCall = matches.some((m) => m.routeId.startsWith("/orgs/$org/on-call"));
const isEscalation = matches.some((m) => m.routeId.startsWith("/orgs/$org/escalation-policies"));
const isDependencies = matches.some((m) => m.routeId.startsWith("/orgs/$org/dependencies"));
```

Add the resolved-name fetches in the same spot the existing fetches
live (L93–102). They're cheap when the param is missing because the
hooks short-circuit on empty UID/slug:

```tsx
const { data: connection } = useConnection(org, params.connectionUid ?? "");
const { data: onCallSchedule } = useOnCallSchedule(org, params.slug ?? "");
const { data: escalationPolicy } = useEscalationPolicy(org, params.slug ?? "");
```

(Verify behavior — if any of the hooks fire for an empty param today,
guard with `enabled: Boolean(uid)` in their definition or pass a
`skipToken` equivalent. Don't paper over it in the breadcrumb.)

#### Channels branch (place after the existing `isStatusPages` block)

```tsx
if (isChannels) {
  const connectionUid = params.connectionUid;
  const isNew = routeIds.has("/orgs/$org/channels/new");
  const channelName = connection?.name || connectionUid?.slice(0, 8);

  return (
    <>
      {connectionUid || isNew ? (
        <Link to="/orgs/$org/channels" params={{ org }} className={linkClass}>
          <Bell className={iconClass} />
          {t("channels")}
        </Link>
      ) : (
        <span className={activeClass}>
          <Bell className={iconClass} />
          {t("channels")}
        </span>
      )}
      {isNew && (
        <>
          <BreadcrumbSeparator />
          <span className={activeClass}>{t("new")}</span>
        </>
      )}
      {connectionUid && (
        <>
          <BreadcrumbSeparator />
          <span className={activeClass}>{channelName}</span>
        </>
      )}
    </>
  );
}
```

#### On-call branch

The detail-route param is `slug`, so we have to disambiguate
on-call's slug from escalation-policies' slug. Use the active section
flag (only one of `isOnCall` / `isEscalation` is true at a time):

```tsx
if (isOnCall) {
  const slug = params.slug;
  const isNew = routeIds.has("/orgs/$org/on-call/new");
  const scheduleName = onCallSchedule?.name || slug;

  return (
    <>
      {slug || isNew ? (
        <Link to="/orgs/$org/on-call" params={{ org }} className={linkClass}>
          <CalendarClock className={iconClass} />
          {t("onCall")}
        </Link>
      ) : (
        <span className={activeClass}>
          <CalendarClock className={iconClass} />
          {t("onCall")}
        </span>
      )}
      {isNew && (
        <>
          <BreadcrumbSeparator />
          <span className={activeClass}>{t("new")}</span>
        </>
      )}
      {slug && (
        <>
          <BreadcrumbSeparator />
          <span className={activeClass}>{scheduleName}</span>
        </>
      )}
    </>
  );
}
```

#### Escalation-policies branch

```tsx
if (isEscalation) {
  const slug = params.slug;
  const isNew = routeIds.has("/orgs/$org/escalation-policies/new");
  const policyName = escalationPolicy?.name || slug;

  return (
    <>
      {slug || isNew ? (
        <Link to="/orgs/$org/escalation-policies" params={{ org }} className={linkClass}>
          <ArrowUpRight className={iconClass} />
          {t("escalationPolicies")}
        </Link>
      ) : (
        <span className={activeClass}>
          <ArrowUpRight className={iconClass} />
          {t("escalationPolicies")}
        </span>
      )}
      {isNew && (
        <>
          <BreadcrumbSeparator />
          <span className={activeClass}>{t("new")}</span>
        </>
      )}
      {slug && (
        <>
          <BreadcrumbSeparator />
          <span className={activeClass}>{policyName}</span>
        </>
      )}
    </>
  );
}
```

#### Dependencies branch

```tsx
if (isDependencies) {
  return (
    <span className={activeClass}>
      <GitBranch className={iconClass} />
      {t("dependencies")}
    </span>
  );
}
```

### Hook side note — slug param collision

Both on-call detail (`on-call.$slug.tsx`) and escalation-policies
detail (`escalation-policies.$slug.tsx`) use the same param name
`slug`. When you're on `/orgs/$org/on-call/foo`, `params.slug === "foo"`
*and* the escalation-policies hook will be called with `"foo"` if not
guarded. That's why the section flags (`isOnCall` / `isEscalation`)
must dispatch first — only render the branch whose flag is on.

If `useOnCallSchedule` / `useEscalationPolicy` fire side-effects on
the wrong slug (e.g., tracking, analytics), pass `enabled` guards:

```tsx
const { data: onCallSchedule } = useOnCallSchedule(org, isOnCall ? params.slug ?? "" : "");
const { data: escalationPolicy } = useEscalationPolicy(org, isEscalation ? params.slug ?? "" : "");
```

Implementer should check the hook bodies first — if they already
short-circuit on empty string (TanStack Query `enabled: Boolean(slug)`),
no guard needed.

## Files to modify

- `web/dash0/src/routes/orgs/$org.tsx` — four new branches + four
  imports + three hook calls.

No translation file edits.
No new components.
No new hooks.

## Verification

Manual + Playwright:

1. Login. Sidebar visible. Click each of: Channels, On-call,
   Escalation policies, Dependencies. For each:
   - Breadcrumb area shows section icon + section title.
2. Channels:
   - Click "+ New channel" → breadcrumb is `Channels > New`. The
     `Channels` segment is a link back to the list.
   - Pick a type, save, land on detail → breadcrumb is
     `Channels > <channel name>`.
3. On-call:
   - Click "Create" → breadcrumb is `On-call > New`.
   - Open an existing schedule → breadcrumb is `On-call > <schedule name>`.
4. Escalation policies:
   - Same as on-call — `Escalation policies > New` and
     `Escalation policies > <policy name>`.
5. Dependencies:
   - Single segment, no sub-segments.
6. Switch FR locale, repeat — labels translate, icons unchanged.
7. Existing breadcrumbs still work (regression check on
   checks / incidents / status-pages / events / badges / account /
   organization / server).

E2E: extend `web/dash0/e2e/` with one short spec walking the four
new sections and asserting the breadcrumb text.

## Critical files

- `web/dash0/src/routes/orgs/$org.tsx` L73–319 — Breadcrumbs.
- `web/dash0/src/components/layout/AppSidebar.tsx` L48–99 — canonical
  source of the section→icon mapping (don't redefine, re-import the
  same lucide icons).
- `web/dash0/src/locales/{en,fr,de,es}/nav.json` — verify all four
  keys exist in every locale (en + fr confirmed; de + es should
  already mirror).
- `web/dash0/src/api/hooks.ts` — `useConnection`, `useOnCallSchedule`,
  `useEscalationPolicy`. Confirm they `enabled`-guard on empty input.
