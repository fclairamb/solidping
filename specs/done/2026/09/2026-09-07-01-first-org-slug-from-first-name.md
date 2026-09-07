---
model: sonnet
effort: medium
---

# The organization `/no-org` proposes to a fresh account lands on a truncated boilerplate slug (`florents-organizatio`, `lorganisation-de-flo`) instead of the person's name

## Problem

Since spec `2026-09-05-01`, a brand-new account that no org adopted arrives on
`/no-org` with the create form pre-filled: "Florent's organization" when we
know a first name, "Bright Falcon" otherwise
([`web/dash0/src/routes/no-org.tsx:56-69`](../../web/dash0/src/routes/no-org.tsx),
[`web/dash0/src/lib/org-name-suggestion.ts`](../../web/dash0/src/lib/org-name-suggestion.ts)).
The card submits with **no slug** and the server derives one from the display
name with `orgslug.GenerateUnique(ctx, db, req.Name)`
([`server/internal/handlers/auth/service.go:3070-3075`](../../server/internal/handlers/auth/service.go)),
whose normalizer drops every non-`[a-z0-9-]` character and caps at 20
([`server/internal/orgslug/orgslug.go:63-97`](../../server/internal/orgslug/orgslug.go)).

The display name is a localized *sentence*, so the slug is the sentence,
truncated. Run through the very `orgSlugify` the preview line uses
([`web/dash0/src/lib/org-slug.ts`](../../web/dash0/src/lib/org-slug.ts)):

| Locale | Proposal (`createOrg.suggestedPersonal`) | Slug the user gets |
|---|---|---|
| en | `Florent's organization` | `florents-organizatio` |
| en | `Alexandra's organization` | `alexandras-organizat` |
| fr | `L'organisation de Florent` | `lorganisation-de-flo` |
| de | `Organisation von Florent` | `organisation-von-flo` |
| es | `Organización de Florent` | `organizacin-de-flore` |

What is wrong with that, for the one screen whose whole job is "here is an
organization for you, click Create":

1. **Every personal org in a locale starts with the same prefix** —
   `lorganisation-de-`, `organisation-von-`, `organizacin-de-`. The prefix is
   the locale's boilerplate; the part that identifies the person sits at the
   end, exactly where the 20-character cap cuts.
2. **The name is what gets truncated.** English cuts mid-word
   (`organizatio`); French and German keep three letters of the first name
   (`flo`), Spanish five. A `Florent` and a `Florence` on a French install
   become `lorganisation-de-flo` and `lorganisation-de-flo2` — a collision
   suffix on top of a truncation.
3. **The UI shows it and seeds it.** The "Will be reachable as …" line prints
   this slug before the user clicks
   ([`create-org-card.tsx:60,138-143`](../../web/dash0/src/components/shared/create-org-card.tsx)),
   and opening "Advanced — customize slug" copies it into the editable field
   (`create-org-card.tsx:151-155`), so the person who wants a better address
   starts from `florents-organizatio` and has to delete it by hand.
4. **Non-Latin first names are worse.** `李雷's organization` normalizes to
   `s-organization`: the name is dropped entirely and the org lives at
   `/orgs/s-organization`.

The random fallback is fine (`Bright Falcon` → `bright-falcon`) — only the
personal proposal is affected. And neither the normalizer nor the 20-char cap
is the bug: the bug is feeding them the sentence when the only meaningful part
of the sentence is the first name.

## Proposal

Keep the localized display name exactly as it is. Derive the **proposed slug
from the first name**, show that in the preview, and let the server keep
owning collision handling — via a slug *hint* the server may normalize and
suffix, as opposed to the strict `slug` it must take literally.

### 1. dash0 — the proposal carries a slug base

- New pure helper next to `suggestOrgName` in `org-name-suggestion.ts`:
  `suggestedSlugBase(suggestion, seed)`:
  - `personal` → `orgSlugify(firstName)`; when that is empty (non-Latin name,
    a name with no `[a-z0-9]` at all) → `orgSlugify(randomOrgName(seed))`, so
    `李雷's organization` lands on `bright-falcon`, not `s-organization`.
  - `random` → `orgSlugify(name)` (unchanged outcome, made explicit).
  Same seed discipline as today: the seed is drawn once into state
  (`no-org.tsx:56`) so the base never reshuffles under the cursor.
- `/no-org` passes both: `<CreateOrgCard suggestedName={…} suggestedSlug={…} />`.
- `CreateOrgCard` gains `suggestedSlug?: string`. The rendered slug becomes:
  typed slug when the slug was touched; `orgSlugify(name)` once the *name* was
  touched (the proposal is gone, so its base must go with it);
  `suggestedSlug` otherwise. The preview line therefore reads `florent`, and
  Advanced seeds the field with `florent` through the existing copy at
  `create-org-card.tsx:151-155` — no change there.
- Submit (`create-org-card.tsx:71-81`): `slug` travels only when the user
  typed one (strict, unchanged). Otherwise send the rendered slug as
  `slugBase`. For a typed name that is `orgSlugify(name)`, i.e. what the
  server would have derived anyway, so the preview stays honest in both
  cases.
- `useCreateOrg` ([`hooks.ts:3517`](../../web/dash0/src/api/hooks.ts)):
  `{ name: string; slug?: string; slugBase?: string }`.
- The second caller,
  [`account.organizations.new.tsx:20`](../../web/dash0/src/routes/orgs/$org/account.organizations.new.tsx),
  passes no suggestion and is unaffected.

### 2. Backend — `slugBase`, a hint rather than a claim

- `CreateOrgRequest` (`service.go:3018-3028`) gains
  `SlugBase string \`json:"slugBase"\``. Semantics, documented on the field:
  normalized by `Slugify`, suffixed `2`, `3`, … on collision, **silently
  ignored when unusable** (falls through to the name) — it never answers 422
  or 409. When both `slug` and `slugBase` are sent, `slug` wins and keeps its
  strict contract; `slugBase` is ignored.
- `CreateOrg` (`service.go:3070-3075`), the whole logic change:
  `slug = orgslug.GenerateUnique(ctx, s.db, req.SlugBase, req.Name)` —
  `GenerateUnique` already tries candidates in order and skips the unusable
  ones (`orgslug.go:106-114`).
- Update the handler comment (`handler.go:739-742`), the OpenAPI
  `CreateOrgRequest` (`openapi.yaml:14835-14848`) and
  [`wiki/api-specification/orgs.md:59`](../../wiki/api-specification/orgs.md)
  with the body shape `{name, slug?, slugBase?}` and the strict-vs-hint
  distinction. No migration, no new error code.

Why the hint field rather than a frontend-only fix: sending `slug: "florent"`
strictly gives the second Florent the 409 that spec `2026-09-05-01` removed
on purpose ("a newcomer who accepted the proposed name never meets a 409 they
cannot act on"), and retrying `florent2`, `florent3` from the browser
re-implements `GenerateUnique` with a race. One optional field is smaller.

### 3. Tests

- `org-name-suggestion.test.ts`: `suggestedSlugBase` — `Florent` → `florent`;
  `Jean-Pierre` → `jean-pierre`; `Alexandra` → `alexandra`; a 25-letter first
  name → capped at 20 with no trailing hyphen; `李雷` → the seeded random
  base (deterministic for a seed, never `""`); `random` → the slug of its
  name.
- Backend handler tests (`handlers/auth`), both dialects as the suite already
  does: `{name:"Florent's organization", slugBase:"florent"}` → 201, slug
  `florent`; same again → `florent2`; `{name:"Acme Corp", slugBase:"!!"}` →
  201, `acme-corp` (unusable hint ignored, not 422); `{name:"Acme",
  slug:"acme-x", slugBase:"acme-y"}` → `acme-x`; `{slugBase:"Florent"}` →
  `florent` (normalized). Positive control: `{slug:"acme-x"}` still 409 when
  taken, `{slug:"a"}` still 422 — the strict path must not soften.
- E2E, the existing "/no-org proposes an organization to a fresh account"
  block in
  [`create-org.spec.ts:168`](../../web/dash0/e2e/create-org.spec.ts): register
  with a first name unique per run (e.g. `Zoe<stamp36>`), then assert the
  preview line reads exactly `zoe<stamp36>`, that opening Advanced seeds the
  field with it, that Create with nothing typed lands on
  `/orgs/zoe<stamp36>` (step 4 today only asserts `/orgs/[^/]+/`), and that
  the org's display name is still the possessive sentence. A unique first
  name is what keeps the run idempotent on a persistent dev database, where
  a fixed `Alice` would drift to `alice2`, `alice3`.
- No new locale keys; the sentences stay.

### Must not change

- The strict `slug` contract (422 invalid, 409 taken) and its
  "never fix up a slug the caller chose" rule (`service.go:3024-3026`).
- `Slugify`'s 20-char cap. Do not "improve" it to cut at a hyphen boundary —
  the input was wrong, not the cap.
- The org's **display name**: "Florent's organization" / "L'organisation de
  Florent" is the localized possessive the earlier spec chose deliberately;
  this spec fixes the address, not the name.

### Open questions

- **Zero-backend alternative**: propose the bare first name (`Florent`) as the
  display name and the slug follows for free. Rejected here because it drops
  the localized possessive and the fr/de/es sentences would still lead with
  boilerplate for anyone who keeps the sentence — but it is the fallback if
  touching `POST /api/v1/orgs` is unwanted.
- `slugBase` on `PATCH /orgs/:org` (rename)? No: a rename is a deliberate
  act and strict is right there.
