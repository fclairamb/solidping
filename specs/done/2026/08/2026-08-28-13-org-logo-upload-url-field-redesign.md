---
model: sonnet
effort: medium
---

# Uploading an org logo fills the URL field with a relative path that the form then rejects

## Problem

On `/dash0/orgs/$org/organization/settings`, the "Organization profile" card
([org-profile-card.tsx](../../web/dash0/src/components/shared/org-profile-card.tsx))
uses a **single text input** both as the "paste an external image URL" field and
as the display of the current logo value.

After a successful upload, the backend deliberately returns the logo as a
**relative** `/pub/assets/<uid>` path (`server/internal/handlers/orglogo/`), and
`applyResult` writes it straight into that input
([org-profile-card.tsx:90](../../web/dash0/src/components/shared/org-profile-card.tsx:90)).
Two problems follow:

1. **The form rejects itself.** The input is `type="url"`
   ([org-profile-card.tsx:256](../../web/dash0/src/components/shared/org-profile-card.tsx:256)),
   so once it contains `/pub/assets/<uid>` the browser's native constraint
   validation refuses to submit the form at all — the user can no longer save a
   name or slug change ("Please enter a URL"). The JS-side guard at
   [org-profile-card.tsx:126-131](../../web/dash0/src/components/shared/org-profile-card.tsx:126)
   (only send `logoUrl` when it starts with `http`) never gets a chance to run,
   because native validation fires before `onSubmit`.
2. **Confusing UX.** An internal storage path shows up in a field whose
   placeholder says `https://example.com/logo.png`, inviting the user to "fix"
   it — and the current mitigation (silently *not* sending the value) is
   invisible magic.

Backend context that must NOT change: `PATCH` profile rejects non-absolute URLs
on purpose — `normalizeLogoURL`
([org_profile.go:287](../../server/internal/handlers/auth/org_profile.go:287))
refuses `/pub/assets/<uid>` because accepting it would let a caller point one
org's logo at another org's file. The fix is frontend-side representation, not a
relaxed server validation.

## Proposal

Redesign the logo control so the two sources — **uploaded file** vs **external
URL** — are represented explicitly instead of sharing one input:

- Track the logo source in component state. When the current `logoUrl` is a
  relative `/pub/assets/…` path (or the profile carries an uploaded-file
  pointer), render it as an **"Uploaded logo"** state: preview thumbnail + a
  small badge/label ("Uploaded file"), the existing red `Trash2` clear button,
  and **do not** put the path into the URL input (leave the input empty, or hide
  it in this state behind a "Use an external URL instead" affordance).
- The URL input only ever holds a user-typed external `http(s)` URL. It keeps
  `type="url"`; since it never receives a relative path any more, native
  validation stops blocking unrelated name/slug saves.
- Typing an external URL while an uploaded logo exists (or vice-versa uploading
  while a URL is set) should clearly switch the source — e.g. a small segmented
  toggle "Upload / URL", or simply last-action-wins with the state label
  updating. Keep it consistent with the status-page branding card
  ([status-page-branding.tsx](../../web/dash0/src/components/shared/status-page-branding.tsx)),
  which has the same upload/URL duality — align the two if it is cheap to do so.
- Follow the design reference
  ([design-reference.tsx](../../web/dash0/src/routes/orgs/$org/design-reference.tsx));
  if the resulting "upload or URL" pattern is a new reusable primitive, add it
  there.
- Keep the submit guard semantics: `logoUrl` is sent only when the user
  explicitly set/cleared an external URL; an uploaded logo is never re-sent
  through `PATCH` (the server would refuse it and clear the upload).

Tests: extend [org-profile.spec.ts](../../web/dash0/e2e/org-profile.spec.ts) —
after an upload, (a) the URL input does not contain `/pub/assets`, (b) saving a
name change still succeeds, (c) the uploaded state is visibly labeled and can be
cleared, (d) switching to an external URL and saving works.

## Open questions

- Exact switch UI (segmented toggle vs last-action-wins with a state label) —
  implementer's choice, guided by the design reference and mobile usability.
