# Check dependencies — dash0 frontend

## Context

Parent spec: `specs/done/2026/05/2026-05-03-57-check-dependencies-and-cascade-rollup.md`. The backend shipped the data model, CRUD endpoints, the rollup hook on incident open, the re-evaluation on incident resolve, and the `?hideSuppressed=` / `?causedByIncidentUid=` filters on the incidents list. dash0 has **zero** references to `causedBy`, `pagingSuppressed`, or `dependencies` — the feature is invisible in the UI.

That's not just a missing-feature problem, it's a correctness-perception problem. Today, an operator who reads the parent spec and realizes the backend exists could go set up dependencies via curl, and from then on **child incidents will silently stop paging when a hard parent is down**. Without UI to surface that suppression, the operator has no way to distinguish "rollup is doing its job" from "paging is broken." The first thing a frontend spec must do is make the suppression visible; only then is shipping the dependency editor net positive.

So this spec bundles the dep editor with the incident-side UI in one go. They're in the same PR; landing one without the other is a regression.

Endpoints we consume (already live):
- `GET    /api/v1/orgs/$org/checks/$check/dependencies` → `{ data: { dependsOn: [...], dependedOnBy: [...] } }`
- `POST   /api/v1/orgs/$org/checks/$check/dependencies` body `{ parentCheckUid, kind, description? }`
- `PATCH  /api/v1/orgs/$org/checks/$check/dependencies/$uid` body `{ kind?, description? }`
- `DELETE /api/v1/orgs/$org/checks/$check/dependencies/$uid`
- `GET    /api/v1/orgs/$org/dependencies` → `{ data: { nodes, edges } }`
- `GET    /api/v1/orgs/$org/incidents?hideSuppressed=true|causedByIncidentUid=$uid`

Backend types (matching `server/internal/handlers/checkdependencies/service.go`):
```ts
interface CheckRef    { uid: string; slug: string; name: string }
interface DependencyEdge {
  uid: string;
  parentCheck: CheckRef;
  childCheck:  CheckRef;
  kind: "hard" | "soft";
  description?: string;
}
interface PerCheckDependencies { dependsOn: DependencyEdge[]; dependedOnBy: DependencyEdge[] }
interface GraphNode  { uid: string; slug: string; name: string }
interface GraphEdge  { uid: string; parentCheckUid: string; childCheckUid: string; kind: "hard"|"soft" }
interface GraphResponse { nodes: GraphNode[]; edges: GraphEdge[] }
```

Existing-error codes mapped to this spec's UX:
| code                       | when                                          | UX                                              |
|----------------------------|-----------------------------------------------|-------------------------------------------------|
| `DEPENDENCY_CYCLE`         | adding edge would create a cycle              | inline error on the Add row, suggest fix        |
| `DEPENDENCY_SELF`          | parent == child                               | client pre-filters; if it slips through, toast  |
| `DEPENDENCY_CROSS_ORG`     | parent in another org (also "not found")      | toast — "check not found"                       |
| `DEPENDENCY_DUPLICATE`     | edge already exists                           | inline error                                    |
| `DEPENDENCY_INVALID_KIND`  | kind != hard/soft                             | shouldn't happen; the picker is constrained     |
| `DEPENDENCY_NOT_FOUND`     | edge gone                                     | invalidate cache, refetch                       |

## Scope

In scope:
1. New API hooks for the five dependency endpoints + the org graph endpoint.
2. **Check detail page** (`checks.$checkUid.index.tsx`): a `<DependenciesCard>` block under the existing "Configuration" / "Last result" grid, with two columns (Depends on / Depended on by). Inline add row at the bottom of the "Depends on" column (one-sided edit — a check can only manage its own parents).
3. **Parent picker** (`<CheckPicker>`): a search-as-you-type popover backed by `useChecks(org, { q })`, with client-side cycle/self/duplicate pre-filtering using the org graph fetched once. Reusable component, lives under `components/shared/`.
4. **Incident detail page** (`incidents.$incidentUid.tsx`): "Caused by" banner when `pagingSuppressed`, "Blast radius" panel when this incident is itself a parent (lookup via `?causedByIncidentUid=`).
5. **Incident list page** (`incidents.index.tsx`): default `hideSuppressed=true`, with a toggle to reveal them; a small badge on rolled-up rows when the toggle is off-then-on.
6. **`IncidentDetail` type** in `api/hooks.ts`: add `causedByIncidentUid?: string`, `pagingSuppressed?: boolean`. (Backend already returns them; the type just doesn't know.)
7. **Org-wide dependency list page** (`dependencies.index.tsx`): plain table from the org graph endpoint, filterable by check name. Linked from the AppSidebar.
8. i18n: new `dependencies` namespace, plus new keys under `incidents` for the rollup banner / blast radius. en/fr/de/es land together (partial translations break the fallback chain — see `2026-05-02-08`).

Out of scope (own future spec, or genuinely not happening):
- **Visual graph diagram.** The parent spec rejected it; nothing has changed. Skip.
- **Deeplinks from a graph node.** Same.
- **Bulk-import of deps from an existing graph file.** Defer until a real demand surfaces.
- **Showing soft-related incidents in a sidebar.** The parent spec tagged this as a soft-edge informational hint. The DB doesn't currently emit a "soft-related" event nor a query helper, and adding one is out of scope here. We render the soft-edge badge in the dep list (so the operator knows what's hard vs informational); the soft-related sidebar can wait until someone asks for it.
- **`paging_suppressed` filter on the per-check incident sub-list.** Page-level toggle on the org list is enough.
- **Editing the description from the incident "Caused by" banner.** Banner is read-only; editing happens on the check detail page.

## API hook additions — `web/dash0/src/api/hooks.ts`

Place near `useChecks` (search for `// Checks hooks`). Response wrappers match what the backend actually returns — `{ data: ... }` for list/graph, bare object for create/update.

```ts
// ---------- types (export them; the picker + cards both consume them) ----------
export type DependencyKind = "hard" | "soft";
export interface CheckRef       { uid: string; slug: string; name: string }
export interface DependencyEdge {
  uid: string;
  parentCheck: CheckRef;
  childCheck: CheckRef;
  kind: DependencyKind;
  description?: string;
}
export interface PerCheckDependencies {
  dependsOn: DependencyEdge[];
  dependedOnBy: DependencyEdge[];
}
export interface GraphNode { uid: string; slug: string; name: string }
export interface GraphEdge {
  uid: string;
  parentCheckUid: string;
  childCheckUid: string;
  kind: DependencyKind;
}
export interface GraphResponse { nodes: GraphNode[]; edges: GraphEdge[] }

// ---------- per-check ----------
export function useCheckDependencies(org: string, checkUid: string) {
  return useQuery({
    queryKey: ["dependencies", org, checkUid],
    queryFn: async () => {
      const r = await apiFetch<{ data: PerCheckDependencies }>(
        `/api/v1/orgs/${org}/checks/${checkUid}/dependencies`,
      );
      return r.data;
    },
    enabled: !!org && !!checkUid,
  });
}

export function useCreateCheckDependency(org: string, checkUid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { parentCheckUid: string; kind: DependencyKind; description?: string }) =>
      apiFetch<DependencyEdge>(
        `/api/v1/orgs/${org}/checks/${checkUid}/dependencies`,
        { method: "POST", body: JSON.stringify(body) },
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["dependencies", org] });
      qc.invalidateQueries({ queryKey: ["dependencyGraph", org] });
    },
  });
}

export function useUpdateCheckDependency(org: string, checkUid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (vars: { uid: string; kind?: DependencyKind; description?: string }) =>
      apiFetch<DependencyEdge>(
        `/api/v1/orgs/${org}/checks/${checkUid}/dependencies/${vars.uid}`,
        { method: "PATCH", body: JSON.stringify({ kind: vars.kind, description: vars.description }) },
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["dependencies", org] });
      qc.invalidateQueries({ queryKey: ["dependencyGraph", org] });
    },
  });
}

export function useDeleteCheckDependency(org: string, checkUid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (uid: string) =>
      apiFetch<void>(
        `/api/v1/orgs/${org}/checks/${checkUid}/dependencies/${uid}`,
        { method: "DELETE" },
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["dependencies", org] });
      qc.invalidateQueries({ queryKey: ["dependencyGraph", org] });
    },
  });
}

// ---------- org graph (single fetch, cached for the picker) ----------
export function useDependencyGraph(org: string, opts?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ["dependencyGraph", org],
    queryFn: async () => {
      const r = await apiFetch<{ data: GraphResponse }>(
        `/api/v1/orgs/${org}/dependencies`,
      );
      return r.data;
    },
    enabled: (opts?.enabled ?? true) && !!org,
    staleTime: 30_000,
  });
}
```

Also extend `IncidentDetail` (line 131):
```ts
export interface IncidentDetail {
  // …existing fields…
  causedByIncidentUid?: string;
  pagingSuppressed?: boolean;
}
```

And add `hideSuppressed` + `causedByIncidentUid` to `useIncidents` options (line 576):
```ts
options?: {
  // …existing…
  hideSuppressed?: boolean;
  causedByIncidentUid?: string;
}
// in the queryFn:
if (options?.hideSuppressed) params.set("hideSuppressed", "true");
if (options?.causedByIncidentUid) params.set("causedByIncidentUid", options.causedByIncidentUid);
```

## `<CheckPicker>` — searchable parent picker

Lives at `web/dash0/src/components/shared/check-picker.tsx`. Reused by the dep-add row and (later) anywhere else we need to pick a check.

```tsx
type CheckPickerProps = {
  org: string;
  value?: string;                // selected check UID
  excludeUids?: Set<string>;     // self + all UIDs that would create a cycle
  onChange: (uid: string | undefined) => void;
  placeholder?: string;
  disabled?: boolean;
};
```

Implementation:
- Build on `<Popover>` + a `<Input>` for typing + a scrollable list. The codebase has `Popover` (`components/ui/popover.tsx`) but no `<Command>` / `cmdk` — don't introduce a new dep just for this.
- On open, fire `useChecks(org, { q: query, limit: 25 })` debounced ~150ms.
- Filter the result list client-side to drop `excludeUids`. Render `name` (or `slug` if no name) + a muted `slug` suffix.
- Keyboard: ↑/↓ to navigate, Enter to select, Esc to close. The `<Input>` keeps focus.
- When `value` is set, the trigger renders the selected check's name + a small ✕ to clear.
- No virtualization — practical orgs have <500 checks; the search filters before render.

This is ~120 lines including the i18n strings; lift it to `components/shared/` so it can later replace ad-hoc `<Select>`-based check pickers (e.g. on the incidents filter row, if we ever add one).

## Cycle/self/duplicate pre-filtering

`<DependenciesCard>` fetches `useDependencyGraph(org)` once on mount. From the graph it precomputes, for the current `checkUid`:

```ts
function ancestorsAndDescendants(graph: GraphResponse, checkUid: string) {
  const adjOut = new Map<string, string[]>(); // parent → children
  const adjIn  = new Map<string, string[]>(); // child  → parents
  for (const e of graph.edges) {
    (adjOut.get(e.parentCheckUid) ?? adjOut.set(e.parentCheckUid, []).get(e.parentCheckUid)!).push(e.childCheckUid);
    (adjIn.get(e.childCheckUid)   ?? adjIn.set(e.childCheckUid,   []).get(e.childCheckUid)!  ).push(e.parentCheckUid);
  }
  const reach = (start: string, adj: Map<string, string[]>) => {
    const seen = new Set<string>();
    const stack = [start];
    while (stack.length) {
      const cur = stack.pop()!;
      for (const next of adj.get(cur) ?? []) {
        if (!seen.has(next)) { seen.add(next); stack.push(next); }
      }
    }
    return seen;
  };
  return {
    descendants: reach(checkUid, adjOut),  // would create a cycle if any of these become a parent
    parentsAlreadySet: new Set((adjIn.get(checkUid) ?? [])), // duplicates
  };
}
```

The picker's `excludeUids = { checkUid (self), ...descendants, ...parentsAlreadySet }`. Server-side validation still wins on race — if the operator's local graph is stale, the POST returns `DEPENDENCY_CYCLE`/`DEPENDENCY_DUPLICATE` and we surface it inline (see error-handling section). The pre-filter is for UX, not safety.

`useDependencyGraph` is invalidated on every successful create/update/delete (already wired in the hooks above), so the picker stays correct without a full reload.

## Check detail page — `<DependenciesCard>`

In `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`, after the existing `grid gap-6 md:grid-cols-2` containing Configuration + Last result (around line 828, before the "Recent results" card), add:

```tsx
<DependenciesCard org={org} checkUid={checkUid} />
```

Component lives at `web/dash0/src/components/checks/dependencies-card.tsx`. Layout:

```
┌─ Dependencies ──────────────────────────────────────────────────────┐
│  Depends on (this check breaks when these break)                    │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │ ● rabbit  [hard]  consumes the orders queue          [edit] ✕ │  │
│  │ ● cdn     [soft]  optional CDN warm-up               [edit] ✕ │  │
│  │ ┌─────────────────────────────────────────────────────────┐   │  │
│  │ │ + Add dependency  [<CheckPicker>] [hard|soft] [desc..]  │   │  │
│  │ └─────────────────────────────────────────────────────────┘   │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                     │
│  Depended on by (these break when this breaks)                      │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │ ● worker-a  [hard]  uses our queue            (read-only) ↗   │  │
│  │ ● worker-b  [hard]                             (read-only) ↗  │  │
│  └───────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────┘
```

Notes:
- The status dot color reuses the same `bg-green-500` / `bg-red-500` / `bg-yellow-500` logic as the page header (line 406). Each row links to that check's detail page (`Link to="/orgs/$org/checks/$checkUid"`).
- Kind badge: `[hard]` uses `bg-red-500/10 text-red-500` (it's the load-bearing one for paging), `[soft]` uses `bg-blue-500/10 text-blue-500`.
- "Depended on by" is **read-only** here — to manage that side, the operator clicks through to the parent's "Depends on" row. One canonical edit surface per edge keeps the model simple.
- Edit: clicking the row's pencil flips it inline to `[<kind dropdown>] [<desc input>] [save] [cancel]`. PATCH on save.
- Delete (✕): an `<AlertDialog>` "Remove dependency on `<parent name>`? This stops cascading-incident rollup for this edge." — same pattern as the existing delete-check dialog (line 552 of the page).
- Empty state: "No dependencies configured. Add a parent above to start cascading-incident rollup."
- The whole card hides itself if dep CRUD returns 403 or the org graph returns 403 (org doesn't yet have any read access; covered by general API auth, not a feature flag — stay consistent with the rest of the page).

Add-row inline error handling:
- `DEPENDENCY_CYCLE`: red helper text under the picker — "This would create a cycle. `<parent>` already depends on `<this check>` (directly or via N hops)." Compute the path locally from the graph and show the chain (`a → b → c → this`) — that's why we keep the graph in cache.
- `DEPENDENCY_DUPLICATE`: red helper text — "Already a parent."
- `DEPENDENCY_CROSS_ORG`: toast "Check not found" (mirrors backend wording).
- Anything else: toast with `error.detail`, fall back to "Failed to add dependency."

## Incident detail page — caused-by + blast radius

`web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx`.

### Caused-by banner (when `incident.pagingSuppressed === true`)

Render at the top of the page, above the title row. Re-use the alert pattern from `2026-05-02-07` (yellow/amber). Body: "Rolled up under **`<parent.checkName>` incident** — paging is suppressed while the root cause is open." Use `useIncident(org, incident.causedByIncidentUid!)` to fetch the parent and link to it. While the parent is loading, the banner reads "Rolled up under a root-cause incident…" — never collapse the banner mid-render to avoid layout jump.

`pagingSuppressed=false && causedByIncidentUid != null` is the post-resolve detached state. In that case render a green-toned info banner "This incident was rolled up under `<parent>` until `<resolvedAt>`. Paging fired then." — meaningful for forensics.

### Blast radius panel (when this incident has children)

Always-attempted query — if the incident has zero children we hide the card:
```ts
const children = useIncidents(org, {
  causedByIncidentUid: incident.uid,
  state: undefined,            // include resolved children too — that's the audit trail
  size: 50,
  refetchInterval: incident.state === "active" ? 30_000 : undefined,
});
```

Render in a card next to the existing incident info:
```
┌─ Blast radius (3 affected) ───────────────────────────────┐
│ ✗ worker-a       (down)         paging suppressed   ↗     │
│ ✗ worker-b       (down)         paging suppressed   ↗     │
│ ✓ web-frontend   (recovered)    rolled up           ↗     │
│                                                           │
│ Paging will fire on any child still down when this        │
│ incident resolves.                                        │
└───────────────────────────────────────────────────────────┘
```

Each row links to the child incident detail page. The check name comes from `child.checkName` (already in the response). Don't try to render the full transitive blast radius (children of children) — the data isn't a tree we walk in the UI; we render only direct rollup-children of this incident.

### Per-check incident sub-list

On the check detail page, the existing `useIncidents(org, { checkUid })` call doesn't filter by suppression. Don't change its default — at the per-check level the operator wants to see *every* incident, including the rolled-up ones (otherwise they'd appear to be missing). Just render a small `[rolled up]` badge on rows where `pagingSuppressed === true`. One-line render change.

## Incident list page — default hide-suppressed + toggle

`web/dash0/src/routes/orgs/$org/incidents.index.tsx`. Add a `hideSuppressed` URL search param (defaults to `true`) and a labelled `<Switch>` "Show rolled-up incidents" in the filter row. Wire to `useIncidents(org, { state, hideSuppressed: !showSuppressed })`. When showing them, paint the `[rolled up]` badge on those rows. The default-true matches the parent spec's recommendation and is the thing on-call cares about — the alert storm goes away by default.

The dashboard's Active Incidents tile (`components/dashboard/...`) should also pass `hideSuppressed: true` so the count reflects what you'd page on, not what's open. Grep for the existing `useIncidents` call in `dashboard/` and add the option.

## Org-wide list page — `dependencies.index.tsx`

`web/dash0/src/routes/orgs/$org/dependencies.index.tsx`. Plain table:

| Parent | → | Child | Kind | Description |

Driven by `useDependencyGraph(org)`. Sortable by parent name / child name; filter input applies to either side. Click a row navigates to the parent check's detail page (the canonical edit surface, per the "Depended on by is read-only" rule above).

Add a sidebar entry under the existing `nav.json` keys (search for `incidents` in `nav.json` to find the pattern). Icon: `GitBranch` from lucide.

This page is the third priority. **If the PR feels too large, defer this page only** — the per-check card still gives discovery, just not a single overview. Don't defer the incident-side UI; that's the load-bearing piece.

## i18n

New `dependencies.json` namespace, en/fr/de/es:

```jsonc
{
  "title": "Dependencies",
  "dependsOn": "Depends on",
  "dependsOnHelp": "This check breaks when these break",
  "dependedOnBy": "Depended on by",
  "dependedOnByHelp": "These break when this breaks",
  "addDependency": "Add dependency",
  "kindHard": "Hard",
  "kindSoft": "Soft",
  "kindHardTooltip": "Failure of this parent suppresses paging on this check",
  "kindSoftTooltip": "Informational — paging still fires",
  "descriptionPlaceholder": "Optional — what is the relationship?",
  "noDependencies": "No dependencies configured. Add a parent above to start cascading-incident rollup.",
  "noDependents": "Nothing depends on this check yet.",
  "remove": "Remove dependency",
  "removeConfirmTitle": "Remove dependency on {{parent}}?",
  "removeConfirmBody": "This stops cascading-incident rollup for this edge.",
  "errors": {
    "cycle": "This would create a cycle. {{path}}",
    "duplicate": "Already a parent.",
    "notFound": "Check not found.",
    "generic": "Failed to add dependency."
  },
  "list": {
    "title": "Dependency graph",
    "empty": "No dependencies in this organization yet.",
    "filter": "Filter by check name",
    "parent": "Parent",
    "child": "Child",
    "kind": "Kind",
    "description": "Description"
  }
}
```

Extend `incidents.json` with:
```jsonc
{
  "rollup": {
    "causedByActive": "Rolled up under <strong>{{parent}}</strong> — paging is suppressed while the root cause is open.",
    "causedByPast":   "Rolled up under {{parent}} until {{resolvedAt}}. Paging fired then.",
    "blastRadiusTitle": "Blast radius ({{count}} affected)",
    "blastRadiusFooter": "Paging will fire on any child still down when this incident resolves.",
    "rolledUpBadge": "rolled up",
    "showRolledUp": "Show rolled-up incidents"
  }
}
```

`<Trans>` (already imported in the incident detail page) handles the `<strong>` interpolation.

Register the new namespace in `src/i18n.ts` alongside the existing namespaces. Match the partial-translation rule: all four locales land in the same commit.

## Verification

1. `make dev-test` running. Backend already has the endpoints.
2. Create three checks against intentionally-bad targets:
   ```bash
   TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
     -d '{"org":"test","email":"test@test.com","password":"test"}' \
     http://localhost:4000/api/v1/auth/login | jq -r '.accessToken')
   for slug in rabbit worker-a worker-b; do
     curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
       -d "{\"slug\":\"$slug\",\"name\":\"$slug\",\"type\":\"http\",\"config\":{\"url\":\"http://127.0.0.1:1\"},\"period\":\"5s\",\"incidentThreshold\":1}" \
       http://localhost:4000/api/v1/orgs/test/checks > /dev/null
   done
   ```
3. UI: Navigate to `/dash0/orgs/test/checks/worker-a`. The Dependencies card is empty. Click "Add dependency", search for `rabbit`, pick it, kind=hard, save. Row appears with green/red status pill.
4. Try to add `worker-a` as a parent of `worker-a` → option is excluded from the picker (self).
5. Add `rabbit` as parent of `worker-b` too. Now navigate to `/dash0/orgs/test/checks/rabbit` — the "Depended on by" column lists `worker-a` and `worker-b`, both read-only.
6. Try to add `worker-a` as a parent of `rabbit` → picker excludes it (descendant). Bypass via curl → API returns `DEPENDENCY_CYCLE` → inline error renders the path `rabbit → worker-a → rabbit`.
7. Wait ~30s. `rabbit` opens an incident first; ~5–10s later the workers fail. Open `/dash0/orgs/test/incidents` — only the rabbit incident is listed (default hides suppressed). Toggle "Show rolled-up incidents" → the worker rows appear, each tagged `[rolled up]`.
8. Open the rabbit incident detail page → Blast radius panel lists `worker-a` and `worker-b` with paging-suppressed pills.
9. Open `worker-a`'s incident detail → yellow "Rolled up under rabbit — paging is suppressed" banner at the top, link points to the rabbit incident.
10. Fix `rabbit` (`PATCH … config.url=https://example.com`). After ~30s the rabbit incident resolves. The worker-a banner switches to the green "rolled up under rabbit until <ts>" form. If worker-a still has a bad URL, its `pagingSuppressed` flips to false and a notification fires (verify in the events tab).
11. `/dash0/orgs/test/dependencies` — table lists both edges.
12. Switch language via `?lang=fr`. The dep card, banner, and blast-radius copy switch.
13. `make build-dash0 lint-dash` clean.

## Risks / unknowns

- **Org-graph cache staleness.** The picker pre-filters using `useDependencyGraph` cached for 30s. Two operators editing simultaneously can produce a graph the picker thinks is fine but the server rejects (cycle/duplicate). The inline error handling above covers it; the worst case is a confused user and a re-render. Acceptable.
- **Suppressed-by-default incident list.** The dashboard count drops the moment this ships. If someone is wedded to the old number, they'll think incidents went missing. Mitigation: the toggle exists; the empty state copy on the list page mentions "rolled-up incidents are hidden by default — toggle 'Show rolled-up' to see them."
- **Picker performance on large orgs.** `useChecks(org, { q, limit: 25 })` returns 25 matches; client filters them against `excludeUids`. Worst case a user types nothing and we render 25 rows, all OK. If an org has so many checks that a literal-empty search returns garbage suggestions, debounce is fine and the user types more characters.
- **`<Command>`/`cmdk` was not introduced.** This was deliberate — the codebase doesn't currently use it and pulling it in for a single picker is a poor trade-off. If a second picker shows up later (heartbeat-source, escalation-target, …) revisit and migrate. The `<CheckPicker>` API is small enough to swap without callers noticing.
- **Soft-related incidents.** The parent spec sketched a "Soft-related incidents" sidebar on the incident detail. Skipping in v1: there's no DB query for it (the rollup walk hard-skips soft edges), and adding one is a server-side change out of this spec's scope. The kind badge in the dep card already tells the operator which edges suppress paging; that's the load-bearing distinction.
- **Translation ergonomics.** `<Trans i18nKey="rollup.causedByActive">` with `<strong>` works because `react-i18next` is already wired with the `<strong>` whitelist; double-check the namespace registration in `i18n.ts` actually lists `dependencies` (test by removing it locally — keys should fall back to literal text, which is the broken state we want to avoid).

## Implementation Plan

Each numbered step is one commit, each green:

1. **API hooks** — add `DependencyKind`, `DependencyEdge`, `useCheckDependencies`, `useCreateCheckDependency`, `useUpdateCheckDependency`, `useDeleteCheckDependency`, `useDependencyGraph`. Extend `IncidentDetail` with `causedByIncidentUid` + `pagingSuppressed`. Extend `useIncidents` options with `hideSuppressed` + `causedByIncidentUid`.
2. **`<CheckPicker>`** — `components/shared/check-picker.tsx`, `<Popover>`-driven, debounced, with `excludeUids`. No call sites yet — just the component + a snapshot test if there's a Vitest harness wired (there isn't today; skip).
3. **`<DependenciesCard>`** — `components/checks/dependencies-card.tsx`. Two columns, inline add row, edit/delete on each row. Wire into `routes/orgs/$org/checks.$checkUid.index.tsx` after the existing grid.
4. **Cycle pre-filter** — `ancestorsAndDescendants` helper inside `dependencies-card.tsx` (or a tiny `lib/dependency-graph.ts` if we like seeing it tested separately). Pipe `excludeUids` to the picker. Render the cycle path on `DEPENDENCY_CYCLE`.
5. **Incident detail UI** — caused-by banner + blast radius panel in `incidents.$incidentUid.tsx`. Use `useIncident` for the parent and `useIncidents(causedByIncidentUid=)` for children.
6. **Incident list toggle** — `hideSuppressed` URL param defaulting to `true`, `<Switch>` in the filter row, `[rolled up]` badge on suppressed rows when shown. Update the dashboard tile to pass `hideSuppressed: true`.
7. **Per-check incidents tab** — render `[rolled up]` badge on rows with `pagingSuppressed === true`. No filter change.
8. **Org-wide list page** — `routes/orgs/$org/dependencies.index.tsx`. Sidebar link in `nav.json` + `components/layout/AppSidebar.tsx`. (Defer first if PR gets fat.)
9. **i18n** — `dependencies.json` for en/fr/de/es; new `rollup` keys in `incidents.json`. Register namespace in `src/i18n.ts`. All four locales in the same commit.
10. **QA + manual run** per the verification checklist. `make build-dash0 lint-dash` clean. Update the parent spec's footer to point here.

Stop conditions during build:
- A user can create a dep but has no UI confirmation that suppression is happening → block. The incident-side UI is non-negotiable.
- Cycle path rendering is wrong (shows fewer hops than the actual cycle) → fix; this is the one diagnostic the operator gets.
- The default-hide-suppressed behavior shipping without the toggle → block. Operators must be able to see what was hidden.
