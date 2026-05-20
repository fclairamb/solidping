# Status updates: UI alignment and section/check scoping

## Context

The v1 admin UI for Status Updates landed in
`specs/todos/2026-05-18-02-status-updates-backend-and-admin.md`. It is
functional but visually inconsistent with the rest of dash0:

- The list page (`/orgs/$org/status-updates`) uses a borderless `<table>`
  inside a `<Card>`, has no column headers, shows **relative** dates
  (`formatDistanceToNow`), has no breadcrumb, and has no search or refresh
  button.
- The edit/new pages render the back arrow and page title **inside the shared
  form component** instead of at the route level — inconsistent with
  On-Call, Channels, Escalation Policies, etc.
- The form does not expose `sectionUid` or `checkUid` even though the
  backend service validates and persists both fields.

Reference designs already in the codebase:

| Page | File |
|---|---|
| Status Pages list | `web/dash0/src/routes/orgs/$org/status-pages.index.tsx` |
| On-Call list | `web/dash0/src/routes/orgs/$org/on-call.index.tsx` |
| On-Call edit | `web/dash0/src/routes/orgs/$org/on-call.$slug.edit.tsx` |
| Breadcrumb wiring | `web/dash0/src/routes/orgs/$org.tsx` — `Breadcrumbs()` function |

## Goals

- **List page**: page header with `Megaphone` icon + subtitle + primary
  "New update" button; search input; Page / Section / Check / Kind filter
  selects; refresh icon button; `<Table>` in `<div className="rounded-md border">`
  with columns Kind / Title / Date (absolute) / actions; ghost icon
  `Pencil` / `Trash2` row actions.
- **Edit page**: route-level header (`h1` on left, `ArrowLeft` ghost button on
  right) matching `on-call.$slug.edit.tsx`; form wrapped in a `<Card>`; no
  breadcrumb in the form itself.
- **New page**: same header pattern as the edit page.
- **Form**: expose optional Section (scoped to the chosen status page) and
  optional Check (scoped to the chosen section, or all page checks if no
  section is chosen), using cascading dropdowns.
- **Breadcrumb**: add a `status-updates` branch in `$org.tsx`'s `Breadcrumbs`
  function mirroring the existing `status-pages` branch.

## Non-goals

- Markdown body preview / WYSIWYG editor.
- i18n namespace for Status Updates (keep English-only until a translation pass).
- Public timeline rendering (`web/status0/` — covered by a separate spec).
- Bulk delete or multi-select.
- Pagination UI (limit + offset on the API is sufficient for now).

## Design

### Files to change

#### 1. `web/dash0/src/routes/orgs/$org/status-updates.index.tsx`

Replace the entire component with the Status Pages / On-Call pattern.

- **Remove** `container mx-auto p-6` wrapper — `OrgLayout` already provides
  the outer padding; other list pages use `<div className="space-y-6">` at
  the root.
- **Remove** the `<Card>` / `<CardHeader>` / `<CardContent>` wrapping the
  table.
- **Header** (`flex items-start justify-between gap-4`):
  ```tsx
  <h1 className="text-3xl font-bold tracking-tight flex items-center gap-2">
    <Megaphone className="h-7 w-7 text-muted-foreground" />
    Status updates
  </h1>
  <p className="text-muted-foreground">
    Publish narrative updates on your status pages.
  </p>
  // Right side:
  <Button asChild>
    <Link to="/orgs/$org/status-updates/new" params={{ org }}
          data-testid="status-updates-new">
      <Plus />
      <span className="hidden sm:inline">New update</span>
    </Link>
  </Button>
  ```
- **Filters row** (`flex flex-wrap items-center gap-2`):
  - `<Input>` with `Search` icon for title search (client-side filter on
    `update.title.toLowerCase().includes(search.toLowerCase())`).
  - **Page** `<Select>` (`data-testid="status-updates-page-filter"`, keep).
  - **Section** `<Select>` (`data-testid="status-updates-section-filter"`, new):
    - Disabled / hidden when Page is `"all"`.
    - Options populated via `useStatusPage(org, pageUid, { with: "sections" })`.
    - First option: value `"all"`, label "All sections".
    - When Page changes, reset Section (and Check) to `"all"`.
  - **Check** `<Select>` (`data-testid="status-updates-check-filter"`, new):
    - Disabled / hidden when Page is `"all"`.
    - When Section is `"all"`: union of all sections' resources from the page.
    - When Section is set: that section's resources only.
    - First option: value `"all"`, label "All checks".
    - When Section changes, reset Check to `"all"`.
  - **Kind** `<Select>` (`data-testid="status-updates-kind-filter"`, keep).
  - Refresh `<Button variant="outline" size="icon">` with `<RefreshCw />`.
- **Table** (wrapped in `<div className="rounded-md border">`):
  - `<TableHeader>` with columns: **Kind** / **Title** / **Date** / (actions col,
    `w-[100px]` no header text).
  - Rows (`data-testid="status-update-row"`):
    - Kind: `<KindBadge kind={u.kind} />` (keep existing component).
    - Title: `<span className="font-medium">{u.title}</span>`.
    - Date: **absolute** — `new Date(u.publishedAt).toLocaleString()` in
      `<span className="text-muted-foreground whitespace-nowrap">`.
    - Actions: ghost icon buttons `Pencil` (link to edit, `data-testid="status-update-row-edit"`)
      + `Trash2` (`text-destructive`, `data-testid="status-update-row-delete"`).
- **Loading**: skeleton rows inside the `rounded-md border` div (not inside a Card).
- **Empty state**: centred muted text with a "New update" CTA button, same
  pattern as `status-pages.index.tsx`.
- **`useStatusUpdates` call**: forward `section` and `check` params when the
  respective filter is not `"all"`.
- **Client-side `kind` filter**: keep as-is (client-side on the fetched list).
- **DeleteAlertDialog**: unchanged logic, just keep it.
- Remove the `formatDistanceToNow` import and dependency.
- Keep the `KindBadge` and `KIND_COLORS` constant (can live in the same file or
  be extracted to `components/shared/kind-badge.tsx` — either is fine).

#### 2. `web/dash0/src/components/shared/status-update-form.tsx`

- **Remove** the inline header block (the `<div className="flex items-center gap-4">` containing `<ArrowLeft />` and `<h1>`). The header is now the responsibility of the route file.
- The form's root element becomes the `<Card>` (drop the outermost `<form className="space-y-6 max-w-2xl">` wrapper; the wrapping `max-w-2xl` and `space-y-6` move to the route).
- `<Card>`:
  - `<CardHeader>`: `<CardTitle>Details</CardTitle>` +
    `<CardDescription>` ("Publish a new update" in create mode; update title in edit mode, passed as a prop).
  - `<CardContent className="space-y-4">`: all existing fields plus the two new ones below.
- **Page field**: show in **both** create and edit mode. In create mode: editable `<Select>`. In edit mode: render the page name as a disabled `<Select>` or a read-only `<Label>` + muted text (page cannot be changed after creation).
- **Section field** (new, optional):
  - `<Label>Section (optional)</Label>`.
  - `<Select>` with sentinel `"none"` for "No section".
  - Populate from `useStatusPage(org, form.statusPageUid, { with: "sections" })` — only fetch when `statusPageUid` is non-empty.
  - When Page changes (create mode), reset `sectionUid` to `"none"`.
  - `data-testid="status-update-form-section"`.
- **Check field** (new, optional):
  - `<Label>Check (optional)</Label>`.
  - `<Select>` with sentinel `"none"` for "No check".
  - Options:
    - If `sectionUid !== "none"`: resources from `page.sections.find(s => s.uid === sectionUid)?.resources ?? []`.
    - If `sectionUid === "none"`: flat list of all resources across all sections (`page.sections.flatMap(s => s.resources ?? [])`).
  - Each option: value = `resource.checkUid`, label = `resource.check?.name ?? resource.checkUid.slice(0, 8)`.
  - When Section changes, reset `checkUid` to `"none"`.
  - `data-testid="status-update-form-check"`.
- **`StatusUpdateFormData`** interface: add `sectionUid: string` and `checkUid: string` (both use `"none"` as the "not set" sentinel within the form; the route file converts them to `undefined` before calling the mutation).
- Existing `data-testid`s on all other fields are **unchanged**.
- The `<form>` submit button row (Cancel + Create/Save) stays as-is, moved to be a sibling of the `<Card>` (rendered by the form component, not the route).

#### 3. `web/dash0/src/routes/orgs/$org/status-updates.new.tsx`

Replace the route content with the On-Call edit pattern:

```tsx
<div className="space-y-6 max-w-2xl">
  <div className="flex items-start justify-between gap-4">
    <h1 className="text-3xl font-bold tracking-tight">New status update</h1>
    <Button asChild variant="ghost" size="icon" aria-label="Back">
      <Link to="/orgs/$org/status-updates" params={{ org }}>
        <ArrowLeft />
      </Link>
    </Button>
  </div>
  <StatusUpdateForm
    org={org}
    mode="create"
    isPending={createMutation.isPending}
    onCancel={() => navigate({ to: "/orgs/$org/status-updates", params: { org } })}
    onSubmit={async (data) => {
      await createMutation.mutateAsync({
        statusPageUid: data.statusPageUid,
        kind: data.kind,
        title: data.title,
        bodyMarkdown: data.bodyMarkdown,
        linkUrl: data.linkUrl || undefined,
        publishedAt: data.publishedAt ? new Date(data.publishedAt).toISOString() : undefined,
        sectionUid: data.sectionUid !== "none" ? data.sectionUid : undefined,
        checkUid:   data.checkUid   !== "none" ? data.checkUid   : undefined,
      });
      toast.success("Status update created");
      navigate({ to: "/orgs/$org/status-updates", params: { org } });
    }}
  />
</div>
```

Remove the `container mx-auto p-6` wrapper.

#### 4. `web/dash0/src/routes/orgs/$org/status-updates.$updateUid.edit.tsx`

Same route-level header pattern:

```tsx
<div className="space-y-6 max-w-2xl">
  <div className="flex items-start justify-between gap-4">
    <h1 className="text-3xl font-bold tracking-tight">Edit status update</h1>
    <Button asChild variant="ghost" size="icon" aria-label="Back">
      <Link to="/orgs/$org/status-updates" params={{ org }}>
        <ArrowLeft />
      </Link>
    </Button>
  </div>
  <StatusUpdateForm
    org={org}
    mode="edit"
    initialData={update}
    isPending={updateMutation.isPending}
    onCancel={() => navigate({ to: "/orgs/$org/status-updates", params: { org } })}
    onSubmit={async (data) => {
      await updateMutation.mutateAsync({
        kind: data.kind,
        title: data.title,
        bodyMarkdown: data.bodyMarkdown,
        linkUrl: data.linkUrl || undefined,
        publishedAt: data.publishedAt ? new Date(data.publishedAt).toISOString() : undefined,
        sectionUid: data.sectionUid !== "none" ? data.sectionUid : undefined,
        checkUid:   data.checkUid   !== "none" ? data.checkUid   : undefined,
      });
      toast.success("Status update saved");
      navigate({ to: "/orgs/$org/status-updates", params: { org } });
    }}
  />
</div>
```

Loading state: match the simpler skeleton in `on-call.$slug.edit.tsx` (muted
"Loading…" or a skeleton block — drop the complex two-skeleton row).

Remove `container mx-auto p-6`.

#### 5. `web/dash0/src/routes/orgs/$org.tsx` — `Breadcrumbs()` function

Add a `useStatusUpdate` hook call alongside the existing ones (near the top of
`Breadcrumbs`, after `useEscalationPolicy`):

```ts
const { data: statusUpdate } = useStatusUpdate(
  org,
  isStatusUpdates ? (params.updateUid ?? "") : "",
);
```

The `enabled` guard inside `useStatusUpdate` is `!!org && !!uid`, so passing
`""` when not in the status-updates section avoids a spurious fetch.

Add the section detection flag:

```ts
const isStatusUpdates = matches.some((m) =>
  m.routeId.startsWith("/orgs/$org/status-updates")
);
```

Add the branch (place it after the `isStatusPages` block, before `isBadges`):

```tsx
if (isStatusUpdates) {
  const updateUid = params.updateUid;
  const isNew  = routeIds.has("/orgs/$org/status-updates/new");
  const isEdit = routeIds.has("/orgs/$org/status-updates/$updateUid/edit");
  const label  = statusUpdate?.title ?? updateUid?.slice(0, 8);

  return (
    <>
      {updateUid || isNew ? (
        <Link to="/orgs/$org/status-updates" params={{ org }} className={linkClass}>
          <Megaphone className={iconClass} />{t("statusUpdates")}
        </Link>
      ) : (
        <span className={activeClass}>
          <Megaphone className={iconClass} />{t("statusUpdates")}
        </span>
      )}
      {isNew && (
        <><BreadcrumbSeparator /><span className={activeClass}>{t("new")}</span></>
      )}
      {updateUid && (
        <><BreadcrumbSeparator /><span className={activeClass}>{label}</span></>
      )}
      {isEdit && (
        <><BreadcrumbSeparator /><span className={activeClass}>{t("edit")}</span></>
      )}
    </>
  );
}
```

Import `Megaphone` from `lucide-react` and `useStatusUpdate` from `@/api/hooks`.
The translation key `"statusUpdates"` already exists in all four locale
`nav.json` files — no translation work needed.

### Preserved `data-testid` attributes

The Playwright suite at `web/dash0/e2e/status-updates.spec.ts` relies on these
IDs — they must survive the redesign unchanged:

| ID | Location after redesign |
|---|---|
| `status-updates-new` | Primary button in the list page header |
| `status-updates-page-filter` | Page `<Select>` in the filters row |
| `status-updates-kind-filter` | Kind `<Select>` in the filters row |
| `status-update-row-edit` | Pencil icon button in each table row |
| `status-update-row-delete` | Trash2 icon button in each table row |
| `status-update-form-title` | Title `<Input>` in the form |
| `status-update-form-body` | Body `<Textarea>` in the form |
| `status-update-form-submit` | Submit button in the form |

New IDs added by this spec (no E2E coverage required in this iteration):

| ID | Location |
|---|---|
| `status-updates-section-filter` | Section `<Select>` in the list filters row |
| `status-updates-check-filter` | Check `<Select>` in the list filters row |
| `status-update-form-section` | Section `<Select>` in the form |
| `status-update-form-check` | Check `<Select>` in the form |

## Verification

1. `make dev` (or `make dev-test`) — visit in browser:
   - `/orgs/default/status-updates`: breadcrumb shows "Status Updates";
     table has column headers Kind / Title / Date / (no heading); dates are
     absolute; refresh button works; filtering by Page activates Section +
     Check selects.
   - `/orgs/default/status-updates/new`: breadcrumb "Status Updates › New";
     `<h1>` left + `<ArrowLeft>` right; Section + Check dropdowns visible
     and cascade correctly; submit sends `sectionUid` / `checkUid` to the
     API (confirm with `GET /api/v1/orgs/default/status-updates`).
   - `/orgs/default/status-updates/<uid>/edit`: breadcrumb "Status Updates ›
     \<title\> › Edit"; Page select disabled; Section + Check pre-populated
     from the stored update and editable.
   - All three pages look correct at 320 px mobile width — "New update" label
     is hidden on small screens, only the `Plus` icon shows.

2. Existing Playwright suite passes without any spec changes:
   ```
   cd web/dash0 && bun run test:e2e -- status-updates.spec.ts
   ```
   (or `make test-dash`)

3. `make lint` — no new ESLint errors.

4. Smoke-test section/check attachment via curl:
   ```bash
   TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
     -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
     'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')
   # Get a status page UID and one of its section + check UIDs
   PAGE=$(curl -s -H "Authorization: Bearer $TOKEN" \
     'http://localhost:4000/api/v1/orgs/default/status-pages?with=sections' \
     | jq -r '.data[0]')
   PAGE_UID=$(echo $PAGE | jq -r '.uid')
   SECTION_UID=$(echo $PAGE | jq -r '.sections[0].uid')
   CHECK_UID=$(echo $PAGE | jq -r '.sections[0].resources[0].checkUid')
   # Create a scoped update
   curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
     -d "{\"statusPageUid\":\"$PAGE_UID\",\"sectionUid\":\"$SECTION_UID\",\"checkUid\":\"$CHECK_UID\",\
          \"kind\":\"info\",\"title\":\"Smoke test\",\"bodyMarkdown\":\"Verifying section+check scoping.\"}" \
     'http://localhost:4000/api/v1/orgs/default/status-updates' | jq '{sectionUid,checkUid}'
   # Expect both fields non-null in the response
   ```
