# Check form is a wall of fields — restructure with progressive disclosure

## Problem

The check create/edit form
([web/dash0/src/components/shared/check-form.tsx](../../web/dash0/src/components/shared/check-form.tsx),
2874 lines) renders as one flat `Card` with three pseudo-sections — "Check
Type", "Configuration", "General" — where "General"
([check-form.tsx:2513-2731](../../web/dash0/src/components/shared/check-form.tsx))
is a single undifferentiated stack of ~12 concerns:

Enabled · Interval · Timeout · Name · Slug · Labels · Notify via ·
Dependencies · Group · Incident Tracking (2 fields) · Flapping (4 fields) ·
Regions

On the edit page
(`/dash0/orgs/$org/checks/$checkUid/edit`) everything is always expanded, so
the page is a ~4000 px scroll on mobile. The information hierarchy is flat:
"which URL do I monitor" sits at the same visual level as "max recovery
multiplier", a tuning knob most users will never touch. The fields people
actually change on a given visit — URL, interval, notification channels —
are scattered across the top, middle, and bottom of the page, and the
Save/Cancel footer is only reachable after scrolling past flapping math.

There is no collapsible/accordion primitive in
[web/dash0/src/components/ui/](../../web/dash0/src/components/ui) yet, and
therefore none in the design reference.

## Proposal

### 1. Reorder by frequency-of-use, not by data model

Target order, top to bottom:

1. **Identity & target** (always visible): Type (create only), the
   protocol config from `renderConfigFields()` (URL/method/expected
   status/host/port…), Name, Enabled switch.
2. **Scheduling** (always visible): Interval, Regions. These decide "how
   is this thing monitored" and are the second-most-edited group.
3. **Notifications** (always visible): `NotifyViaSection` — alerting is
   the whole point of the product; it should not live below the fold.
4. **Collapsible, collapsed by default** (see §2 for the collapse rules):
   - **Authentication & secrets** — Basic-Auth username/password, secret
     headers (protocol-specific extras that today inflate "Configuration").
   - **Organization** — Slug, Labels, Group. Slug is auto-generated and
     rarely touched after creation.
   - **Dependencies** — `DependsOnFormSection`.
   - **Incident tracking** — confirmation/recovery periods
     ([check-form.tsx:2649-2683](../../web/dash0/src/components/shared/check-form.tsx)).
   - **Flapping** — all four tuning fields
     ([check-form.tsx:2685-2715](../../web/dash0/src/components/shared/check-form.tsx)).
   - **Advanced** — Timeout (and future expert knobs).

### 2. Collapsed ≠ hidden: summarize on the header

Add a `CollapsibleSection` primitive to `components/ui/` (Radix
`Collapsible` is already a transitive dep of the shadcn stack) and register
it in the design reference
([design-reference.tsx](../../web/dash0/src/routes/orgs/$org/design-reference.tsx))
per the repo convention. Key behaviors, in priority order:

- **Value summary in the header.** A collapsed section's header line shows
  its current effective state, e.g. `Flapping — window 6h, cooldown ×5
  (defaults)` or `Incident tracking — confirm 120s, recover 120s ·
  2 customized`. This is what makes collapsing safe: nothing is invisible,
  it's just compressed. Sections whose values deviate from defaults also get
  a small "customized" badge/dot.
- **Auto-expand on error.** A section containing a field with a validation
  error (`fieldErrors`, `slugError`) must force-expand on submit and scroll
  into view. Collapsible UIs that swallow errors are worse than the wall.
- **Open state driven by content.** Default expanded if the section holds
  non-default values on load (an edit page for a check with flapping tuned
  should show that tuning); collapsed otherwise.
- **Sticky footer.** Keep Cancel / Save Changes pinned to the viewport
  bottom on long forms so saving never requires scrolling.

### 3. Code organization: separate check-type modules from the general form

The 2874-line component is the real disease; the wall-of-fields UI is a
symptom. Today every check type (~35 of them) is smeared across **four
parallel per-type blocks** inside `CheckForm`, which must be kept in sync
by hand:

1. **State loading** — ~96 flat `useState` hooks seeded from
   `initialData.config`
   ([check-form.tsx:404+](../../web/dash0/src/components/shared/check-form.tsx)),
   plus the same config→state mapping repeated in `applySample()`
   ([check-form.tsx:634](../../web/dash0/src/components/shared/check-form.tsx)).
   Type-specific fields like `rdpWarningDays` or `sleepMs` are hoisted into
   the shared component and live there for every render of every type.
2. **`currentConfig` useMemo** — `switch (type)` state→config mapping
   ([check-form.tsx:705](../../web/dash0/src/components/shared/check-form.tsx)).
3. **`handleSubmit`** — a *second, duplicated* `switch (type)` state→config
   mapping with validation interleaved
   ([check-form.tsx:964](../../web/dash0/src/components/shared/check-form.tsx)).
   Two copies of the same serialization logic is a standing invitation for
   the preview and the submitted payload to disagree.
4. **`renderConfigFields`** — `switch (type)` over JSX
   ([check-form.tsx:1302](../../web/dash0/src/components/shared/check-form.tsx)).

Adding a check type today means touching all four sites; forgetting one
compiles fine and fails at runtime.

**Proposed structure** — one module per check type behind a common
interface, plus a registry:

```
components/checks/form/
  check-form.tsx        # layout, general sections, submit orchestration
  sections/             # general collapsible sections from §1
    identity-target.tsx
    scheduling.tsx
    notifications.tsx     (move NotifyViaSection here)
    dependencies.tsx      (move DependsOnFormSection here)
    organization.tsx
    incident-tracking.tsx
    flapping.tsx
  types/
    index.ts            # registry: Record<CheckType, CheckTypeModule>
    http.tsx
    tcp.tsx             # tcp/udp/ftp share one module
    dns.tsx
    database.tsx        # postgresql/mysql/mssql/oracle share
    …
```

```ts
interface CheckTypeModule<S = unknown> {
  types: CheckType[];              // e.g. ["tcp", "udp", "ftp"]
  fromConfig(config: CheckConfig): S;          // replaces per-type useState seeding + applySample
  toConfig(state: S): { config: CheckConfig; errors: FieldErrors };  // single source for preview AND submit
  Fields: React.FC<{ state: S; onChange(next: S): void; errors: FieldErrors }>;
}
```

`CheckForm` then holds **one** `configState` object for the active type
(re-seeded via `fromConfig` on type change) instead of 96 hooks, calls
`toConfig` once for both `currentConfig` and `handleSubmit` (killing the
duplication), and renders `module.Fields`. The general form — name, slug,
labels, interval, notify, dependencies, incident tracking, flapping,
regions — knows nothing about protocols; a new check type becomes a
one-file change plus a registry entry.

Migration is mechanical and can land one type per commit: HTTP first (the
most complex — basic auth, secret headers, assertions), then the long
tail, which is mostly host/port variations. Preserve existing
`data-testid`s throughout so the Playwright suite keeps passing unchanged.

### 4. Honest opinion / creative options considered

- **Tabs were considered and rejected**: tabs hide validation errors in
  non-active tabs and break the "scan the whole config before saving"
  review flow. One scrollable column with collapsible sections preserves
  review-ability.
- **A side "table of contents" nav** (anchor links to sections, visible on
  `lg:` screens) is a cheap, nice-to-have addition once sections exist —
  optional, second PR.
- Per the repo convention "a page's core navigation belongs in the URL",
  supporting `?section=flapping` to deep-link with that section expanded
  and scrolled-to is desirable (e.g. for docs and support links) — keep it
  `replace: true` like other incidental refinements.

### 5. Side note — pre-fill from GET parameters

This **already exists** on `/checks/new`:
[checks.new.tsx:16-32](../../web/dash0/src/routes/orgs/$org/checks.new.tsx)
whitelists `checkType`, `checkPeriod`, `checkName`, `checkSlug`, `httpUrl`,
`httpMethod`, `host`, `port`, `url`, `domain`, `username`, `database` and
maps them into `CheckForm`'s `initialData`. Remaining work:

- **Extend the whitelist** to the missing common fields: expected status,
  timeout, labels (`label=key:value`, repeatable/comma-separated per the
  API convention), regions, group slug, and confirmation/recovery periods.
- **Document it** in the docs site (`web/docs/`) — today the feature is
  undiscoverable; it is ideal for "add this to monitoring" links from
  READMEs, CLIs, and the empty-state onboarding.
- **Scope stays `/new` only.** Pre-filling the *edit* form from query
  params would silently mutate an existing check's form state from a URL —
  surprising and phishing-adjacent. Not wanted.

## Notes / open questions

- Should "Enabled" stay in the always-visible block or move into the
  header as a switch next to Save? Leaning header — it's a state toggle,
  not configuration.
- Collapsed-by-default for **Organization** hides Slug on create, where it
  is generated from Name anyway; confirm the E2E tests
  ([web/dash0/e2e/](../../web/dash0/e2e)) that fill slug/labels are updated
  to expand sections first (`data-testid` on section triggers).
- The existing `data-testid` attributes must survive the extraction so
  current Playwright specs keep passing with minimal churn.
- With the §3 module registry in place, the §5 query-param whitelist can
  eventually be derived from each module's `fromConfig` keys instead of
  being maintained by hand in `checks.new.tsx`.

## Implementation Plan

Sequenced to land in green, revertible commits. The form is refactored
**in place** in `components/shared/check-form.tsx` (avoids route/import churn);
the §3 module directory is established for the serialization single-source.

1. **§2 primitive** — add `components/ui/collapsible.tsx` (Radix
   `@radix-ui/react-collapsible` wrapper) and `components/ui/collapsible-section.tsx`
   (`CollapsibleSection`: header title + value-summary line + "customized"
   dot/badge, controlled/uncontrolled open state, auto-scroll on open, id for
   deep-link). Register both in `design-reference.tsx` (new section + SECTIONS
   entry). Commit.
2. **§1 reorg** — restructure the `CheckForm` render into: Identity & target
   (type + protocol config + name, always visible), Scheduling (interval +
   regions), Notifications (NotifyViaSection), then collapsed-by-default
   `CollapsibleSection`s: Authentication & secrets (basic-auth / secret headers
   — HTTP for now), Organization (slug + labels + group), Dependencies,
   Incident tracking, Flapping, Advanced (timeout). Enabled → header switch.
   Sticky Cancel/Save footer. Auto-expand + scroll a section holding a
   validation error on submit; open-by-default when it holds non-default
   values. `?section=<name>` deep-link (expand + scroll, `replace: true`),
   `/new` and edit. Preserve every `data-testid`. Commit.
3. **§3 single-source serialization** — establish `components/checks/form/` and
   extract `serializeCheckConfig(type, state, { forSubmit })` into
   `serialize.ts`, collapsing the duplicated `currentConfig` useMemo and
   `handleSubmit` switch into ONE function used by BOTH preview and submit
   (kills the preview/payload-disagreement risk). Per-type `Fields`/`fromConfig`
   module registry for all ~35 types is noted as follow-up. Commit.
4. **§5 query-param prefill** — extend `checks.new.tsx` `validateSearch` +
   `initialData` with expected status, timeout, labels (`label=key:value`,
   repeatable/comma-separated), regions (`region=a,b`), group slug (resolve →
   uid), confirmation/recovery periods. `/new` only. Commit. Then document in
   `web/docs/`. Commit.
5. **E2E + QA** — author `check-form-progressive-disclosure.spec.ts` (collapsed
   section expands on error; non-default section loads expanded; save via
   sticky footer). Update existing specs that fill slug/labels to expand the
   Organization section first via its trigger `data-testid`. Run
   `make build-dash0`, `bun run lint`, `make build-docs`. Final "all checks
   passing" commit.
