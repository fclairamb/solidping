# Check export format v2 — defaults block, human durations, deterministic ordering

## Problem

The check export document (`GET /orgs/:org/checks/export`,
[server.go:658](server/internal/app/server.go:658), types in
[service.go:2195-2244](server/internal/handlers/checks/service.go:2195)) is a
faithful DB dump rather than a document a human can read, diff, or write. On a
real export (~65 checks) the problems compound:

1. **Default noise dominates the file.** Six alerting fields
   (`confirmationPeriodSeconds: 120`, `escalationThreshold: 10`,
   `recoveryPeriodSeconds: 120`, `flappingWindowSeconds: 21600`,
   `flapBackoffFactor: 2`, `maxRecoveryMultiplier: 8`) repeat identically on
   nearly every check — well over half the file's bytes. The two checks that
   actually deviate (dnsbl: backoff 5, multiplier 10, plus
   `reopenCooldownMultiplier`) are invisible in the noise. Root cause: these
   values are materialized per-row at creation
   ([check.go:114](server/internal/db/models/check.go:114),
   [check.go:159](server/internal/db/models/check.go:159)), so the exporter
   ([service.go:2350-2364](server/internal/handlers/checks/service.go:2350))
   can't distinguish "user chose 120" from "default was 120" and dumps
   everything.

2. **Two duration dialects.** `period` is a clock string (`"00:00:30"`,
   `"06:00:00"`) while every other duration is a `...Seconds` integer
   (`21600`). Neither reads well: `"06:00:00"` doesn't obviously mean 6 hours,
   and `21600` requires mental math.

3. **`"enabled": true` on every check.** The field has no `omitempty`
   ([service.go:2224](server/internal/handlers/checks/service.go:2224))
   because a plain bool with `omitempty` would drop the meaningful `false`.

4. **Non-deterministic, non-topological ordering.** Checks are emitted in
   `ListChecks` order, so children appear before the parents they depend on
   and related checks are scattered. Exports committed to git produce noisy
   diffs.

5. **`_secretsStripped` naming.** Hardcoded `true`
   ([service.go:2403](server/internal/handlers/checks/service.go:2403)),
   negatively named, underscore-prefixed, and buried at the end of the
   document.

6. **Single-line output.** The endpoint emits one ~20 KB line; the whole point
   of a portable export is that humans look at it.

## Proposal

Introduce **export document version 2**. The importer
([service.go:2446](server/internal/handlers/checks/service.go:2446)) accepts
both v1 and v2; the exporter emits v2 only. Explicitly **out of scope**: any
change to the `dependsOn` edge shape — it stays
`[{"parentSlug": "...", "kind": "hard"}]` in v2.

### 1. Top-level `defaults` block, per-check overrides only

Add a `defaults` object to `ExportDocument` holding the document-wide value
for: `regions`, `period`, `confirmationPeriod`, `escalationThreshold`,
`recoveryPeriod`, `flappingWindow`, `flapBackoffFactor`,
`maxRecoveryMultiplier`. Each check then omits any field equal to the
default.

- **Export**: compute each default as the *modal* (most frequent) value across
  the exported checks, so the per-check overrides are minimal regardless of
  how system defaults drift. Emitting the defaults in the document keeps the
  export self-describing: a future change to system defaults cannot silently
  alter what a reimport produces.
- **Import (v2)**: resolution order per field = check value → document
  `defaults` value → current system default (same as today for absent
  fields). A v2 document without a `defaults` block is valid.
- **Import (v1)**: unchanged — all fields explicit per check.

Target shape:

```json
{
  "version": 2,
  "exportedAt": "2026-07-10T17:24:30Z",
  "organization": "acmetech",
  "secrets": "stripped",
  "defaults": {
    "regions": ["default"],
    "period": "1m",
    "confirmationPeriod": "2m",
    "escalationThreshold": 10,
    "recoveryPeriod": "2m",
    "flappingWindow": "6h",
    "flapBackoffFactor": 2,
    "maxRecoveryMultiplier": 8
  },
  "checks": [
    {
      "name": "api.acme.io (dnsbl)",
      "slug": "dnsbl-acme-io",
      "type": "dnsbl",
      "config": { "target": "api.acme.io" },
      "regions": ["default", "eu-2", "us-1"],
      "period": "1h",
      "reopenCooldownMultiplier": 5,
      "flapBackoffFactor": 5,
      "maxRecoveryMultiplier": 10
    }
  ]
}
```

### 2. One duration format everywhere

All durations in v2 are Go-style duration strings (`"30s"`, `"1m"`, `"6h"`,
`"90s"`), both in `defaults` and per check:

- `period`: `"00:00:30"` → `"30s"` (exporter renders the canonical compact
  form; importer accepts anything `time.ParseDuration` accepts, and — for
  compatibility — the v1 `HH:MM:SS` clock form).
- `confirmationPeriodSeconds` → `confirmationPeriod` (`"2m"`),
  `recoveryPeriodSeconds` → `recoveryPeriod` (`"2m"`),
  `flappingWindowSeconds` → `flappingWindow` (`"6h"`).
- Unitless fields keep their names and integer form: `escalationThreshold`,
  `flapBackoffFactor`, `maxRecoveryMultiplier`, `reopenCooldownMultiplier`.

Rendering rule: largest unit that divides evenly, single unit preferred
(`120` → `"2m"`, `21600` → `"6h"`, `90` → `"90s"` or `"1m30s"` — pick one
rule and test it).

### 3. Invert `enabled` → `disabled,omitempty`

v2 checks carry `"disabled": true` only for disabled checks; the common
enabled case is silent. v1 import keeps reading `enabled`. (v2 import treats
an absent `disabled` as enabled.)

### 4. Deterministic ordering

Export sorts checks by `group` (empty group last or first — pick one, test
it), then `slug`. Dependencies are already sorted by `parentSlug`
([service.go:2338-2342](server/internal/handlers/checks/service.go:2338)).
Import must not require topological order (verify with a test where a child
precedes its parent — the existing two-pass import should already handle
this).

### 5. Rename the secrets marker

`_secretsStripped: true` → `"secrets": "stripped"` (string enum, room for a
future `"included"` if a trusted-transport export ever exists). Emitted near
the top of the document alongside `version`/`organization` rather than last.

### 6. Pretty-print the export

The export endpoint emits indented JSON (2 spaces). If payload size ever
matters, gzip covers it; a `?pretty=false` opt-out is not needed for v1 of
this change.

## Touchpoints

- `ExportDocument` / `ExportCheck` types and `ExportChecks`
  ([service.go:2195-2405](server/internal/handlers/checks/service.go:2195)) —
  new v2 types or versioned marshalling.
- `ImportChecks` version gate
  ([service.go:2446](server/internal/handlers/checks/service.go:2446)) —
  accept `1` and `2`, dispatch field resolution accordingly.
- **Apply path**: `POST /orgs/:org/checks/apply`
  ([server.go:660](server/internal/app/server.go:660)) consumes the same
  `ExportCheck` shape (including `previousSlug`,
  [service.go:2218](server/internal/handlers/checks/service.go:2218)) — apply
  manifests must get the same v1/v2 handling so a fresh export can be
  round-tripped through apply.
- Export handler response writing (pretty-print).
- OpenAPI spec (`server/internal/app/openapi/openapi.yaml`) and any generated
  client (`server/pkg/client/client_generated.go`) touched by the schema
  change.
- Docs site if the export/import format is documented there
  (`web/docs/`).
- Dash0: if the UI has an export/import screen, confirm it treats the
  document as opaque JSON (likely) — no change expected.

## Tests

- Round-trip: export v2 → import into a clean org → re-export → byte-identical
  documents (proves ordering determinism + defaults stability).
- v1 document import still works, including the `HH:MM:SS` period form and
  explicit `enabled`.
- Duration rendering table test (`30` → `"30s"`, `120` → `"2m"`, `21600` →
  `"6h"`, `90` → chosen rule).
- Defaults resolution: check value beats document default beats system
  default; v2 doc without `defaults` imports correctly.
- `disabled` semantics: absent → enabled; `true` → disabled; v1 `enabled:
  false` still imports as disabled.
- Child-before-parent ordering in the input document imports successfully.

## Out of scope

- `dependsOn` shorthand (string-array form) — explicitly excluded.
- Fixing the double-prefixed slugs (`ssl-ssl-...`) in existing data — that's a
  slug-generation/data bug to chase separately.

## Implementation Plan

Design principle: keep the canonical `ExportDocument`/`ExportCheck` Go structs as
the in-memory representation that the import/apply business logic consumes, and add
a separate **v2 wire layer** for JSON in/out. Import stays version-agnostic below
the decode boundary — a custom `ExportDocument.UnmarshalJSON` peeks at `version`,
decodes v1 or v2, and resolves defaults so downstream code always sees concrete
per-check values.

### Step 1 — `timeutils.Duration.Scan` accepts Go-style durations
Add a `time.ParseDuration` fallback (after ISO8601 / `HH:MM:SS`) so the v2 period
string `"30s"`/`"2m"` scans through the existing create/update path. Table-test it.

### Step 2 — canonical `ExportCheck`: pointer-ize the defaulted fields
Change `ConfirmationPeriodSeconds`, `RecoveryPeriodSeconds`, `EscalationThreshold`,
`FlappingWindowSeconds`, `FlapBackoffFactor`, `MaxRecoveryMultiplier` to `*int`
(`ReopenCooldownMultiplier` already is) so "absent" is distinguishable from an
explicit value. Rename `SecretsStripped bool json:"_secretsStripped"` to
`Secrets string json:"secrets"` on `ExportDocument`. Update export-build to set
`&check.X` and `Secrets = "stripped"`, and the apply round-trip test helper.

### Step 3 — v2 wire types + marshalling (`export_v2.go`)
- `formatDurationSecondsCompact(seconds)` → `"30s"`/`"2m"`/`"6h"` (single unit,
  largest that divides evenly; `90` → `"90s"`). Table test.
- `durationStringToSeconds(s)` → parse `"2m"`/`HH:MM:SS`/ISO8601 → seconds.
- `exportDocumentV2` / `exportDefaultsV2` / `exportCheckV2` with v2 JSON tags
  (`disabled,omitempty`, string durations, `defaults`, `secrets`).
- `buildExportDocumentV2(doc)`: compute **modal** default per field across the
  checks, emit the `defaults` block, and per check omit any field equal to the
  default; invert `enabled`→`disabled`.
- `marshalExportDocumentV2(doc)`: pretty-print (2-space indent).

### Step 4 — export path
`ExportChecks` sets `Version = 2`, `Secrets = "stripped"`, and sorts checks by
`(group, slug)` with empty group last. Handler `ExportChecks` writes
`marshalExportDocumentV2(doc)` bytes instead of `WriteJSON(doc)`.

### Step 5 — import/apply decode + version gate
- `ExportDocument.UnmarshalJSON`: peek `version`; v1 → decode via alias (current
  behavior); v2 → decode wire, resolve each check field (check → doc default →
  absent) into canonical pointers, invert `disabled`→`Enabled`.
- `ImportChecks` and `ApplyChecks`: accept version `1` and `2`.

### Step 6 — persist the alerting fields on import
Extend `UpsertCheckRequest` with `Regions`, `ReopenCooldownMultiplier`,
`FlappingWindowSeconds`, `FlapBackoffFactor`, `MaxRecoveryMultiplier` (it already
has confirmation/recovery). Wire them through `UpsertCheck` into both the
`UpdateCheckRequest` and `CreateCheckRequest` it builds, and populate them in
`importSingleCheck` from the resolved `ExportCheck`. (`EscalationThreshold` is not
settable anywhere in the create/update/DB path today, so it is emitted in
`defaults` for self-description but not plumbed — every check keeps the system
default, which preserves round-trip fidelity.)

### Step 7 — tests
Round-trip byte-identical (normalizing `exportedAt`); v1 import (`HH:MM:SS` +
explicit `enabled`); duration rendering table; defaults resolution (check beats doc
default beats system default; v2 doc without `defaults`); `disabled` semantics;
child-before-parent ordering. OpenAPI/docs/dash0 need no change (endpoints aren't in
OpenAPI; dash0 treats the doc as opaque JSON).
