# Jobs section breadcrumbs: show "Jobs", extend it on detail pages

## Context

The admin **Jobs** section currently renders **no breadcrumb at all**. The
`Breadcrumbs` component in
[`web/dash0/src/routes/orgs/$org.tsx`](../../web/dash0/src/routes/orgs/$org.tsx)
has a per-section branch for every other area (checks, incidents, events,
status pages, status updates, discovery, organization, server, …) but none for
`/orgs/$org/jobs`. With no matching branch the function falls through to its
final `return null`, so all three Jobs routes show an empty breadcrumb:

- `https://solidping.k8xp.com/dash0/orgs/default/jobs?tab=jobs` — the list page
  ([`jobs.index.tsx`](../../web/dash0/src/routes/orgs/$org/jobs.index.tsx))
- `https://solidping.k8xp.com/dash0/orgs/default/jobs/check/<uid>?allOrgs=false`
  — the check-schedule job detail page
  ([`jobs.check.$checkJobUid.tsx`](../../web/dash0/src/routes/orgs/$org/jobs.check.$checkJobUid.tsx))
- `https://solidping.k8xp.com/dash0/orgs/default/jobs/<uid>` — the background-job
  detail page ([`jobs.$jobUid.tsx`](../../web/dash0/src/routes/orgs/$org/jobs.$jobUid.tsx))

The list page should show **`Jobs`** in the breadcrumb. On a detail page the
breadcrumb should be **extended** with a leaf crumb — i.e. `Jobs › <leaf>` —
exactly like the existing checks / incidents / discovery branches do.

This is purely the shared breadcrumb bar (rendered once by the org layout); the
in-page back-arrow button on the detail pages is separate and stays as-is.

## Goals

- **List page** (`/orgs/$org/jobs/`): breadcrumb shows a single active crumb
  `Jobs` with the `Workflow` icon (same icon the sidebar uses for this entry —
  see `AppSidebar.tsx:222`).
- **Check-job detail** (`/orgs/$org/jobs/check/$checkJobUid`): breadcrumb shows
  `Jobs › <check name>`, where `Jobs` is a link back to the list and the leaf is
  the check's name (fallback to the first 8 chars of the uid).
- **Background-job detail** (`/orgs/$org/jobs/$jobUid`): breadcrumb shows
  `Jobs › <job type>`, where `Jobs` links back and the leaf is the job `type`
  (fallback to the first 8 chars of the uid).
- The `Jobs` link **preserves the `allOrgs` scope** when it is set, mirroring the
  detail pages' own back buttons (which navigate with
  `search: allOrgs ? { tab: "jobs", allOrgs: true } : { tab: "jobs" }`).
- Use the existing `nav` translation key `jobs` (already present in
  `en/de/es/fr` — EN/DE = "Jobs", FR = "Tâches", ES = "Tareas"). No new strings.

## Out of scope

- Any change to the page bodies, the in-page back-arrow buttons, or the page
  content of the detail pages ("extended" here refers to the **breadcrumb**, not
  adding fields/cards to the detail view).
- The sidebar, the `PageHeader` on the list page, or the Jobs admin-guard layout
  ([`jobs.tsx`](../../web/dash0/src/routes/orgs/$org/jobs.tsx)).
- New API endpoints — all needed data is already fetchable via existing hooks.

## Implementation

All changes are in
[`web/dash0/src/routes/orgs/$org.tsx`](../../web/dash0/src/routes/orgs/$org.tsx)
inside the `Breadcrumbs` component (the `OrgLayout` wiring already renders
`<Breadcrumbs org={org} />`).

### 1. Import the `Workflow` icon

Add `Workflow` to the `lucide-react` import block (lines 11–32) — keep the list
alphabetical (after `User2`/before close, matching existing ordering tolerance).

### 2. Import the two detail hooks

The component already imports section hooks (`useCheck`, `useIncident`,
`useStatusPage`, …) from `@/api/hooks`. Add `useCheckJob` and `useBackgroundJob`
to that import group. Their signatures (already in `hooks.ts`):

```ts
useCheckJob(org, uid, { allOrgs })       // → { data: CheckScheduleJob }
useBackgroundJob(org, uid, { allOrgs })  // → { data: BackgroundJob }
```

`CheckScheduleJob.checkName` is `string | null`; `BackgroundJob.type` is a
string. Both hooks are gated on a truthy `uid`, so passing `""` when the branch
is inactive disables the fetch (same short-circuit trick used for
`useOnCallSchedule` / `useEscalationPolicy`).

### 3. Detect the Jobs section and its sub-routes

Alongside the other `is…` flags near the top of `Breadcrumbs`:

```ts
const isJobs = matches.some((m) => m.routeId.startsWith("/orgs/$org/jobs"));
const isCheckJobDetail = routeIds.has("/orgs/$org/jobs/check/$checkJobUid");
const isBackgroundJobDetail = routeIds.has("/orgs/$org/jobs/$jobUid");
```

`params` (the merged match params) already exposes `checkJobUid` and `jobUid`.

### 4. Resolve the `allOrgs` scope from the active match's search

The `Jobs` link must preserve `allOrgs`. Read it from whichever Jobs match is
active (the detail routes declare `allOrgs?: boolean` in their `validateSearch`):

```ts
const jobsMatch = isJobs
  ? matches.find((m) => m.routeId.startsWith("/orgs/$org/jobs"))
  : undefined;
const jobsAllOrgs = Boolean(
  (jobsMatch?.search as { allOrgs?: boolean } | undefined)?.allOrgs,
);
```

### 5. Fetch the leaf labels (only when on a detail route)

```ts
const { data: checkJob } = useCheckJob(
  org,
  isCheckJobDetail ? (params.checkJobUid ?? "") : "",
  { allOrgs: jobsAllOrgs },
);
const { data: backgroundJob } = useBackgroundJob(
  org,
  isBackgroundJobDetail ? (params.jobUid ?? "") : "",
  { allOrgs: jobsAllOrgs },
);
```

### 6. Render the Jobs branch

Add a branch (placed near the other section branches, e.g. just after the
`isDiscovery` block). The `Jobs` crumb is an active `<span>` on the list page and
a `<Link>` on the detail pages, matching the checks/incidents pattern:

```tsx
if (isJobs) {
  const isDetail = isCheckJobDetail || isBackgroundJobDetail;
  const leaf = isCheckJobDetail
    ? (checkJob?.checkName || params.checkJobUid?.slice(0, 8))
    : isBackgroundJobDetail
      ? (backgroundJob?.type || params.jobUid?.slice(0, 8))
      : null;

  return (
    <>
      {isDetail ? (
        <Link
          to="/orgs/$org/jobs"
          params={{ org }}
          search={jobsAllOrgs ? { tab: "jobs", allOrgs: true } : { tab: "jobs" }}
          className={linkClass}
        >
          <Workflow className={iconClass} />
          {t("jobs")}
        </Link>
      ) : (
        <span className={activeClass}>
          <Workflow className={iconClass} />
          {t("jobs")}
        </span>
      )}
      {isDetail && (
        <>
          <BreadcrumbSeparator />
          <span className={activeClass}>{leaf}</span>
        </>
      )}
    </>
  );
}
```

`t` here is the `nav` namespace translator already in scope; `linkClass`,
`activeClass`, `iconClass`, and `BreadcrumbSeparator` are the shared helpers used
by every other branch.

## Verification

1. `make dev-test` (or run the dash0 dev server against the backend) and sign in
   as an admin.
2. **List page** — open `/dash0/orgs/default/jobs?tab=jobs`: breadcrumb reads
   `Jobs` (Workflow icon), as an active (non-link) crumb.
3. **Check-job detail** — from the **Check schedule** tab click a row (e.g.
   `/dash0/orgs/default/jobs/check/<uid>?allOrgs=false`): breadcrumb reads
   `Jobs › <check name>`; clicking `Jobs` returns to the list on the `jobs` tab.
4. **Background-job detail** — from the **Background jobs** tab click a row:
   breadcrumb reads `Jobs › <type>`; `Jobs` link returns to the list.
5. **Scope preservation** — as a super-admin, switch to **All orgs**, open a
   detail page (URL carries `allOrgs=true`), and confirm the `Jobs` breadcrumb
   link round-trips back with the All-orgs scope still selected.
6. Leaf fallback: if the name/type hasn't loaded yet, the leaf shows the first 8
   chars of the uid (no blank crumb, no crash).
7. `bun run lint` and `bun run build` pass in `web/dash0` (tsc is a hard gate).

## Files referenced

- `web/dash0/src/routes/orgs/$org.tsx` — the only file changed (Breadcrumbs).
- `web/dash0/src/api/hooks.ts` — `useCheckJob`, `useBackgroundJob`,
  `CheckScheduleJob`, `BackgroundJob` (read-only; no change).
- `web/dash0/src/components/layout/AppSidebar.tsx` — reference for the `Workflow`
  icon + `nav:jobs` label (no change).
- `web/dash0/src/locales/{en,de,es,fr}/nav.json` — existing `jobs` key (no change).

## Implementation Plan

All work is in `web/dash0/src/routes/orgs/$org.tsx`, inside the `Breadcrumbs`
component. Single commit (one file), broken into the spec's six steps:

1. **Imports** — add `Workflow` to the `lucide-react` import block, and add
   `useCheckJob` + `useBackgroundJob` to the `@/api/hooks` import group.
2. **Section flags** — add `isJobs`, `isCheckJobDetail`, `isBackgroundJobDetail`
   alongside the other `is…` flags at the top of `Breadcrumbs`.
3. **Scope resolution** — derive `jobsMatch` / `jobsAllOrgs` from the active Jobs
   match's `search` so the `Jobs` link preserves `allOrgs`.
4. **Leaf fetches** — call `useCheckJob` / `useBackgroundJob`, each gated on its
   detail flag (pass `""` otherwise to disable), with `{ allOrgs: jobsAllOrgs }`.
5. **Render branch** — add the `if (isJobs) { … }` branch (after `isDiscovery`):
   active `Jobs` span on the list page, `<Link>` back to `/orgs/$org/jobs`
   (search preserves `allOrgs`, `tab: "jobs"`) + leaf crumb on detail pages.
   Leaf = `checkJob.checkName` / `backgroundJob.type`, falling back to the first
   8 chars of the uid.

QA: `make build-client lint-back test` + dash0 `bun run lint` / tsc gate.
