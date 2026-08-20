---
model: sonnet
effort: high
---

# Editing a status update silently discards most of the changes

## Problem

On `/dash0/orgs/$org/status-updates/$updateUid/edit`, changing fields and hitting
**Save changes** shows the "updated" toast, but re-opening the page shows the old
values — and for some fields the value really never changed in the database.

There are three distinct defects stacked on top of each other, which is why the
symptom looks like "most parameters are not taken into account".

### 1. The single-update query cache is never invalidated (stale UI)

`useUpdateStatusUpdate` only invalidates the **list** key:

```ts
// web/dash0/src/api/hooks.ts:4482
onSuccess: () => {
  queryClient.invalidateQueries({ queryKey: ["statusUpdates", org] });
},
```

The edit page reads through `useStatusUpdate` → `["statusUpdate", org, uid]`
([hooks.ts:4450](web/dash0/src/api/hooks.ts:4450)), which is **never** invalidated.
With the app-wide `staleTime: 1000 * 60`
([main.tsx:57](web/dash0/src/main.tsx:57)), navigating back into the edit page
within a minute of saving re-renders the form from the *pre-edit* cached payload.
Every field looks reverted, including the ones that were actually persisted.

`useCreateStatusUpdate` and `useDeleteStatusUpdate` have the same narrow
invalidation, and the detail/list surfaces that key off a status update
(`statusPage`, `incident`) are not refreshed either.

### 2. Clearing an optional field is impossible (data genuinely not saved)

The edit submit handler maps "empty" to `undefined`:

```ts
// web/dash0/src/routes/orgs/$org/status-updates.$updateUid.edit.tsx:31-38
linkUrl: data.linkUrl || undefined,
sectionUid: data.sectionUid !== "none" ? data.sectionUid : undefined,
checkUid: data.checkUid !== "none" ? data.checkUid : undefined,
```

`JSON.stringify` drops `undefined` keys, so the PATCH body simply omits the
field. The backend treats an absent field as "leave untouched"
(`if req.SectionUID != nil { … }`, `server/internal/handlers/statusupdates/service.go`),
so switching **Section** back to *No section*, **Check** back to *No check*, or
emptying **Link URL** is a silent no-op — with a success toast on top.

`publishedAt` has the same shape (`data.publishedAt ? … : undefined`), though
the input is always populated in practice.

### 3. The API has no way to express "clear this field" either

Even a well-behaved client cannot fix defect 2 today. `UpdateStatusUpdateRequest`
uses plain `*string` fields
([service.go:119-129](server/internal/handlers/statusupdates/service.go:119)), so a
JSON `null` and an omitted key both decode to a nil pointer and are
indistinguishable. `sectionUid`, `checkUid`, `incidentUid` and `linkUrl` are all
nullable columns that can never be un-set through the API.

The repo already has the pattern for this: `statuspages/handler.go` decodes the
body into a `map[string]json.RawMessage` to detect key *presence* separately from
value (`parseSettingsField`,
[handler.go:59](server/internal/handlers/statuspages/handler.go:59)).

## Proposal

### Backend — presence-aware PATCH for the nullable fields

In `server/internal/handlers/statusupdates/`, make `PATCH
/api/v1/orgs/:org/status-updates/:uid` distinguish *absent* from *explicit null*
for `sectionUid`, `checkUid`, `incidentUid` and `linkUrl`, following the
`parseSettingsField` presence-map pattern already used by `statuspages`:

- absent key → leave the column untouched (unchanged behaviour);
- `null` → set the column to `NULL`;
- non-empty string → validate and set as today.

Treat an explicit empty string `""` as `null` for `linkUrl` (a browser input
yields `""`, not `null`) rather than storing an empty string; for the UID fields
`""` should be a `VALIDATION_ERROR`, since it is never a legal UID.

Clearing `sectionUid` must not implicitly clear `checkUid` server-side — the
client decides. Keep the existing validation (section belongs to the page, check
is a resource of the page) for the set-a-value path.

Update `server/internal/app/openapi/openapi.yaml` so the four fields are declared
nullable on the PATCH request, and note the semantics in
`wiki/api-specification/`.

### Frontend — actually send the cleared values

In [status-updates.$updateUid.edit.tsx](web/dash0/src/routes/orgs/$org/status-updates.$updateUid.edit.tsx:29),
send `null` instead of `undefined` for cleared optional fields, and widen
`UpdateStatusUpdateRequest` in [hooks.ts:4405](web/dash0/src/api/hooks.ts:4405) to
`string | null` for `sectionUid`, `checkUid`, `incidentUid`, `linkUrl`. Always
send `publishedAt` from the form.

Guard the section/check pairing in the form: when the selected check no longer
belongs to the selected section, reset the check rather than submitting an
inconsistent pair (the current `handleSectionChange` already resets on section
change — make sure edit-mode initial state can't start out inconsistent either).

### Frontend — invalidate the right query keys

`useUpdateStatusUpdate` must invalidate `["statusUpdate", org, uid]` in addition
to `["statusUpdates", org]`. `useDeleteStatusUpdate` should also remove/invalidate
the singular key. Apply the same review to `useCreateStatusUpdate` for any
surface that lists updates for a status page or incident.

### Tests

- Backend: table-driven cases on `UpdateStatusUpdate` proving each of
  *absent → unchanged*, *null → cleared*, *value → set*, for all four nullable
  fields, plus positive controls (a field really was non-null before the clear,
  and a sibling field really is untouched by the clear). Cover both the SQLite
  and Postgres services.
- Handler test that a PATCH body of `{"sectionUid": null}` reaches the service as
  "clear" while `{}` reaches it as "untouched".
- Dash0 E2E (`web/dash0/e2e/`): edit an update that has a section, a check and a
  link URL → clear all three → save → reload the edit page and assert the fields
  read *No section* / *No check* / empty, i.e. the assertion must prove the
  *negative* rather than just that the request succeeded. A second pass that
  changes the values (rather than clearing them) guards the stale-cache
  regression: save, navigate to the list, re-enter the edit page within the
  1-minute `staleTime` window and assert the new values are shown.
