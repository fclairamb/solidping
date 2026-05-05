# Render the result time in the breadcrumb on result detail pages

## Context

Opening a single check result, e.g.

```
/dash0/orgs/default/checks/d431b62a-…/results/019df729-…
```

renders the dash0 detail page from
`web/dash0/src/routes/orgs/$org/checks.$checkUid.results.$resultUid.tsx`.

The page itself shows the period and metrics, but the **breadcrumb** in the
top bar stops at the check name:

```
Checks  ›  api.example.com
```

There is no third segment for the result, so the user has no breadcrumb-level
indication of *which* result they are looking at — only the URL UID and the
in-page heading (`Result 019df729`) tell them. Other detail routes in this
project add a third breadcrumb segment (incidents show the check name,
status pages show the page name); results should follow the same pattern.

The single in-page hint we have today is `data.uid?.slice(0, 8)`, which is
not human-meaningful. The natural label is the result's **period start**
timestamp (already loaded by `useResult`), which is what users actually
remember when navigating between results.

## Scope

In scope:

1. Extend the `isChecks` branch of `Breadcrumbs` in
   `web/dash0/src/routes/orgs/$org.tsx` to handle the result-detail route
   (`/orgs/$org/checks/$checkUid/results/$resultUid`).
2. When that route is matched:
   - keep the existing `Checks › <check name>` chain,
   - turn `<check name>` into a link back to the check detail page (it is
     currently a non-link `<span>` because the check page is the leaf),
   - append a third segment showing the result's period start as a localized
     date/time (active, non-link).
3. Use the existing `useResult(org, checkUid, resultUid)` hook to fetch the
   result so the timestamp comes from the same source as the page body — no
   extra round-trip, no risk of disagreement with the page heading.
4. Keep the existing `Result {uid}` heading inside the page; the breadcrumb
   change is additive, not a replacement.

Out of scope:

- Changing the in-page heading on the result detail route.
- Reworking the breadcrumb component for non-result routes.
- Adding a status-pages-style icon for results.
- Public status page (`web/status0`) — it has no breadcrumb.

## Implementation

### `web/dash0/src/routes/orgs/$org.tsx`

Inside `Breadcrumbs`:

1. Detect the result route alongside the existing check-edit / new-check
   detection:

   ```ts
   const isCheckResult = routeIds.has(
     "/orgs/$org/checks/$checkUid/results/$resultUid",
   );
   const resultUid = params.resultUid;
   ```

2. Fetch the result conditionally — pass empty strings when not on the
   route so the hook short-circuits, mirroring the existing
   `useCheck(org, params.checkUid ?? "")` pattern:

   ```ts
   const { data: result } = useResult(
     org,
     params.checkUid ?? "",
     params.resultUid ?? "",
   );
   ```

   (Verify `useResult`'s `enabled`/early-return behaviour while implementing.
   If it always fetches, gate it with the route check before adding it.)

3. In the `isChecks` branch, when `checkUid` is present, make the check-name
   segment a link whenever the leaf is *not* the check detail page — that
   already covers `isCheckEdit`, and now also `isCheckResult`:

   ```tsx
   {checkUid && (
     <>
       <BreadcrumbSeparator />
       {isCheckEdit || isCheckResult ? (
         <Link
           to="/orgs/$org/checks/$checkUid"
           params={{ org, checkUid }}
           search={{ graphPeriod: undefined, graphFull: undefined }}
           className={linkClass}
         >
           {checkName}
         </Link>
       ) : (
         <span className={activeClass}>{checkName}</span>
       )}
     </>
   )}
   ```

4. Append the result segment after the check-edit branch:

   ```tsx
   {isCheckResult && (
     <>
       <BreadcrumbSeparator />
       <span className={activeClass}>
         {result?.periodStart
           ? new Date(result.periodStart).toLocaleString()
           : (resultUid?.slice(0, 8) ?? "")}
       </span>
     </>
   )}
   ```

   The UID-slice fallback covers the brief moment before the result loads,
   so the breadcrumb never collapses to two segments and then expands to
   three (which would visibly shift the layout).

### Translations

No new copy is needed: the segment is a date/time, not a label.

## Test plan

`make dev-test`, log in, open a check that has at least one result.

1. Navigate to the check detail page → breadcrumb is
   `Checks › <check name>` (regression check, unchanged).
2. Click a point on the response-time chart to navigate to a result detail
   page → breadcrumb becomes
   `Checks › <check name> › <localized timestamp>`.
3. The middle segment (`<check name>`) is now a link → clicking it returns
   to the check detail page.
4. Hard-refresh the result detail URL → on first paint the third segment
   shows the UID prefix, then swaps to the timestamp once `useResult`
   resolves; no layout jump beyond text change.
5. Open a result for an aggregated period (Day / Week tab → click a bar)
   → timestamp matches the period start shown in the page body.
6. Navigate from a result back to checks list, then into a different check,
   then into one of its results → breadcrumb updates to the new check name
   and new timestamp (no stale data from the previous result).

## Risks

- `useResult` may not currently support empty UID arguments. If it always
  fires the request, the breadcrumb component will issue an unnecessary API
  call on every page. Gate the call with
  `enabled: routeIds.has("/orgs/$org/checks/$checkUid/results/$resultUid")`
  or equivalent — verify the hook's signature before settling on the form.
- `toLocaleString()` produces locale-dependent output; the breadcrumb may
  end up wider than expected. The breadcrumb bar already wraps long check
  names without breaking, so this should be fine, but verify visually on
  narrow viewports.

## References

- `web/dash0/src/routes/orgs/$org.tsx` (Breadcrumbs component, lines ~79–147)
- `web/dash0/src/routes/orgs/$org/checks.$checkUid.results.$resultUid.tsx`
- `web/dash0/src/api/hooks` (`useResult`, `useCheck`)
- Example URL from 2026-05-05 conversation:
  `/dash0/orgs/default/checks/d431b62a-6c25-49eb-8d05-9d8a58ccce8d/results/019df729-527d-7ab2-82a4-47c797f323a0`
