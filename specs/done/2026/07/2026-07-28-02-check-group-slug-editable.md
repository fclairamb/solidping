---
model: sonnet
effort: medium
---

# The check group slug can't be changed from the dashboard, even though DevOps scripts address groups by slug

## Problem

Check groups are addressable by slug: `GET/PATCH/DELETE /api/v1/orgs/:org/check-groups/:uid`
resolves its path parameter through `GetCheckGroupByUidOrSlug`
([service.go:242](server/internal/handlers/checkgroups/service.go:242)), and the slug is
what shows up in incident payloads as `checkGroupSlug`
([incidents/service.go:1675](server/internal/handlers/incidents/service.go:1675)). That makes
the slug the stable, human-writable identifier people put in DevOps scripts, CI jobs and
Terraform-ish tooling — not the UUID.

But the dashboard never lets a user set or change it:

- **Create**: the "New group" dialog only asks for a name
  ([checks.index.tsx:991-1012](web/dash0/src/routes/orgs/$org/checks.index.tsx:991)), so the
  slug is always auto-derived by `sanitizeSlug(name)`
  ([service.go:51](server/internal/handlers/checkgroups/service.go:51)). A group named
  "Prod — EU (west)" silently becomes `prod-eu-west`.
- **Edit**: the edit page exposes name, description and escalation policy only
  ([check-groups.$uid.edit.tsx:223-249](web/dash0/src/routes/orgs/$org/check-groups.$uid.edit.tsx:223)),
  and its submit handler sends just `{ name, description, escalationPolicyUid }`
  ([check-groups.$uid.edit.tsx:113-119](web/dash0/src/routes/orgs/$org/check-groups.$uid.edit.tsx:113)).
  The slug isn't even displayed, so an operator can't discover what to type in their script
  without hitting the API.

The backend already supports it end to end — `UpdateCheckGroupRequest.Slug`
([service.go:112](server/internal/handlers/checkgroups/service.go:112)) validates format,
rejects UUID-shaped values and returns `ErrSlugConflict` on duplicates
([service.go:248-263](server/internal/handlers/checkgroups/service.go:248)), and it is
declared in the OpenAPI schema ([openapi.yaml:8764](server/internal/app/openapi/openapi.yaml:8764)).
So this is a pure UI gap. It's also an inconsistency: the check form *does* let you edit a
check's slug ([check-form.tsx:667](web/dash0/src/components/shared/check-form.tsx:667)).

## Proposal

Expose the slug in both check-group surfaces, following the **Name + slug pair** pattern
already documented in the design reference
([design-reference.tsx:1232-1318](web/dash0/src/routes/orgs/$org/design-reference.tsx:1232)):
name first, slug auto-filled from it via `slugify()` until the user types into the slug
field, then never clobbered again. No separate "edit slug" toggle.

1. **Edit page** ([check-groups.$uid.edit.tsx](web/dash0/src/routes/orgs/$org/check-groups.$uid.edit.tsx))
   - Add a `slug` field to the Details card, seeded from `group.slug`. Because this is an
     *existing* group, treat the slug as already user-owned: do **not** auto-derive it from
     name edits (`slugManuallyEdited` starts `true`), otherwise renaming a group would
     silently rewrite the identifier scripts depend on.
   - Include `slug` in `CheckGroupEditFormSubmit` and in the `updateGroup.mutateAsync` payload.
   - Validate client-side against the **group** rule — `^[a-z][a-z0-9-]{2,39}$` (3–40 chars),
     matching `slugRegex` ([service.go:19](server/internal/handlers/checkgroups/service.go:19)).
     Note this is *not* the check form's 3–50 rule
     ([check-form.tsx:299](web/dash0/src/components/shared/check-form.tsx:299)) — don't copy
     that regex.
   - Surface the server's 409/validation errors inline on the slug field (`ErrSlugConflict`,
     `ErrInvalidSlugFormat` already map to field-level validation errors in
     [handler.go:142-177](server/internal/handlers/checkgroups/handler.go:142)).
   - Add a short helper line under the field explaining that the slug is usable in API URLs
     and that changing it breaks existing references — this is the whole point of the change,
     so the warning has to be visible, not buried.

2. **Create dialog** ([checks.index.tsx:991](web/dash0/src/routes/orgs/$org/checks.index.tsx:991))
   - Add an optional slug input under the name, auto-filled via `slugify(name)` and
     detaching on manual edit. Pass it through to `useCreateCheckGroup`; when left untouched
     and empty, keep sending no slug so the server keeps deriving it.

3. **i18n**: add the new keys (`groupForm.slug`, `groupForm.slugHelp`,
   `groupForm.slugInvalid`, `groupForm.slugTaken`, and the create-dialog equivalents under
   `dialog.*`) to all four locales — `en`, `fr`, `de`, `es` in `web/dash0/src/locales/`.
   `groupForm.detailsDescription` currently reads "Name and description for this check
   group." and needs updating.

4. **Tests**
   - Extend the Playwright coverage in
     [web/dash0/e2e/check-groups.spec.ts](web/dash0/e2e/check-groups.spec.ts): create a group
     with an explicit slug; edit a group's slug and assert the group is then reachable by the
     new slug (`GET .../check-groups/<new-slug>`); assert a duplicate slug shows an inline
     error rather than a toast-only failure; assert that editing only the **name** of an
     existing group leaves its slug untouched (the regression this spec most needs to
     prevent).
   - Backend behaviour is already implemented; add a Go test only if `UpdateCheckGroup`
     slug-change coverage turns out to be missing.

5. **Docs**: mention that a check group's slug is editable and is the recommended stable
   identifier for scripting, wherever check groups are documented in `web/docs/`.

### Out of scope

No redirect/alias mechanism for old slugs — a changed slug simply stops resolving, same as
for checks today. The inline warning is the mitigation.
