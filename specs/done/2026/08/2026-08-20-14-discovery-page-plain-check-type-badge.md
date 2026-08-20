---
model: sonnet
effort: low
---

# Discovery scan page still renders check types as a plain grey badge, bypassing the check-type identity registry

## Problem

Spec `2026-08-20-12-check-type-identity-registry` made
[check-type-identity.tsx](../../web/dash0/src/components/shared/check-type-identity.tsx)
the single source of truth for "which check type is this", and converted every
check-type rendering in dash0 to the registry-driven `CheckTypeBadge` — the checks
list, the check detail header, the type picker, and the design reference.

One call site was missed. The discovery scan detail page renders each suggested
check's type as an untinted, un-uppercased secondary badge:

```tsx
// web/dash0/src/routes/orgs/$org/discovery.$jobUid.index.tsx:275
<Badge variant="secondary" className="text-xs">{check.type}</Badge>
```

So a discovered `postgresql` suggestion shows a grey `postgresql` chip here, while
the very same check shows an indigo `POSTGRESQL` chip on the checks list the moment
it is promoted. That is the one remaining place where the same concept has two
different visual identities, which is exactly what the registry exists to prevent —
and it also means a new check type added to the registry silently fails to reach
this page.

## Proposal

Replace the plain badge at
[discovery.$jobUid.index.tsx:275](../../web/dash0/src/routes/orgs/$org/discovery.$jobUid.index.tsx)
with the registry-driven badge:

```tsx
<CheckTypeBadge type={check.type} />
```

importing `CheckTypeBadge` from `@/components/shared/check-type-identity`.

Details:

- **Text-first, no icon** — matching the checks list. `CheckTypeBadge` is already
  deliberately icon-free (`check-type-identity.tsx:211-218`); do not add a
  `CheckTypeIcon` alongside it. The row is a dense multi-column label
  (checkbox + name + type + config hint) and a glyph there is noise.
- **Drop the local `className="text-xs"`** — the badge carries its own canonical
  chip sizing (`text-[10px] font-mono font-medium uppercase px-1.5 py-0.5`,
  `check-type-identity.tsx:209`). The chip will render slightly smaller and
  uppercase; that is the intended consistency, not a regression.
- **Keep the `Badge` import** — the same file still uses it for the "Promoted"
  chip a few lines below (`discovery.$jobUid.index.tsx:284`).
- Leave `data-testid="discovery-check-row"` and every other test hook untouched.

### Verification

- Existing discovery E2E (`web/dash0/e2e/discovery.spec.ts`,
  `discovery-promote.spec.ts`) must still pass. They locate rows with
  Playwright `filter({ hasText: … })`, which is a case-insensitive substring
  match, so `"ICMP"` keeps matching an `icmp` type. The one thing to watch is a
  registry label that is not a case variant of the raw type (e.g. `postgresql` →
  `PostgreSQL` is fine, but a re-worded label would not be): if any discovery
  spec filters on a type string whose registry label differs by more than case,
  update that filter.
- Scoped QA: `make build-dash0`, then `cd web/dash0 && bun run lint`. The lint
  gate is **no NEW errors in the touched file** — dash0 carries ~44 pre-existing
  `react-hooks` errors on base; do not fix that debt here and do not relax the
  eslint config.

### Out of scope

- Any change to the registry itself, its tones, or its icons.
- Other surfaces (status0, docs site) and any backend change.
