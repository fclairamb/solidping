# Check Export & Import Specification

## Overview

Allow users to export all their checks as a JSON file and import them back into the same or a different SolidPing instance. This enables easy migration between instances, backup/restore of monitoring configurations, and sharing check setups across teams or organizations.

The export format is a self-contained JSON document that captures everything needed to recreate checks — without instance-specific data like UIDs, timestamps, or status.

## API Endpoints

### Export

`GET /api/v1/orgs/:org/checks/export`

Returns all checks for the organization as a downloadable JSON document.

**Query Parameters (all optional):**
- `type` — filter by check type (e.g., `http`, `tcp`). Comma-separated for multiple.
- `labels` — filter by labels (format: `key1:value1,key2:value2`)
- `checkGroupUid` — filter by group UID, or `none` for ungrouped

**Response:** `200 OK` with `Content-Type: application/json` and `Content-Disposition: attachment; filename="solidping-checks-{org}-{date}.json"`

### Import

`POST /api/v1/orgs/:org/checks/import`

Accepts a JSON document in the export format and creates or updates checks by slug.

**Query Parameters:**
- `dryRun` (bool, default: false) — preview what would happen without applying changes

**Request Body:** The export JSON format (see below).

**Response:**
```json
{
  "created": 3,
  "updated": 2,
  "skipped": 0,
  "errors": [
    {
      "index": 4,
      "slug": "bad-check",
      "error": "invalid config: url is required"
    }
  ]
}
```

**Behavior:**
- Matches existing checks by `slug` (upsert semantics)
- If a check with the same slug exists → update it
- If no check with that slug exists → create it
- Check groups referenced by `group` name are auto-created if they don't exist
- Each check is processed independently — one failure doesn't block others
- When `dryRun=true`, the response shows what *would* happen but makes no changes

## JSON Export Format

```json
{
  "version": 1,
  "exportedAt": "2026-03-22T14:30:00Z",
  "organization": "default",
  "checks": [
    {
      "name": "Google Homepage",
      "slug": "http-google",
      "description": "Monitor Google homepage availability",
      "type": "http",
      "config": {
        "url": "https://google.com",
        "method": "GET",
        "timeout": "10s"
      },
      "regions": ["eu-1", "us-1"],
      "labels": {
        "env": "prod",
        "team": "platform"
      },
      "enabled": true,
      "period": "1m",
      "group": "Search Engines",
      "incidentThreshold": 3,
      "escalationThreshold": 10,
      "recoveryThreshold": 3,
      "reopenCooldownMultiplier": null,
      "maxAdaptiveIncrease": null
    }
  ]
}
```

### Field Descriptions

**Top-level:**
- `version` (int, required) — format version, currently `1`. Allows future format evolution.
- `exportedAt` (string) — ISO 8601 timestamp of export. Informational only.
- `organization` (string) — source org slug. Informational only (import target is the URL path org).
- `checks` (array, required) — the check definitions.

**Per check:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Human-readable check name |
| `slug` | string | yes | Unique identifier within org (used for upsert matching) |
| `description` | string | no | Optional description |
| `type` | string | yes | Check type: `http`, `tcp`, `icmp`, `dns`, `ssl`, `heartbeat`, `domain`, `smtp` |
| `config` | object | yes | Type-specific configuration |
| `regions` | string[] | no | Execution regions. If omitted, uses org defaults. |
| `labels` | object | no | Key-value labels |
| `enabled` | bool | no | Default: `true` |
| `period` | string | no | Check frequency (e.g., `"1m"`, `"30s"`). Default: `"1m"` |
| `group` | string | no | Check group name (not UID). Auto-created on import if missing. |
| `incidentThreshold` | int | no | Failures before incident. Default: 3 |
| `escalationThreshold` | int | no | Failures before escalation. Default: 10 |
| `recoveryThreshold` | int | no | Successes before recovery. Default: 3 |
| `reopenCooldownMultiplier` | int | no | Adaptive cooldown multiplier |
| `maxAdaptiveIncrease` | int | no | Adaptive increase cap |

### What is NOT included

- **UIDs** — generated fresh on import
- **Timestamps** (createdAt, updatedAt) — set on import
- **Status fields** (status, statusStreak, statusChangedAt) — determined by actual checks
- **Connections** — org-specific notification/integration bindings
- **Results/history** — monitoring data is not portable

## Validation Rules

### Import Validation
- `version` must be `1` (or supported version)
- `checks` array must not be empty
- Each check must have a valid `slug` (3-20 chars, lowercase, starts with letter)
- Each check must have a valid `type`
- `config` is validated by the corresponding checker's `Validate()` method
- `period` must parse as a valid duration if provided
- `group` names are matched case-insensitively to existing groups

### Error Handling
- Invalid top-level format → `400 VALIDATION_ERROR` immediately
- Per-check errors are collected and returned in the `errors` array
- Successfully imported checks are committed even if some fail
- The response always includes total counts so the user knows the outcome

## Dashboard UI

### Export

Add an "Export" button in the checks list page toolbar (next to "New Check").

**Behavior:**
1. Click "Export" → calls `GET /api/v1/orgs/:org/checks/export`
2. Browser downloads the JSON file
3. If filters are active (type, labels, group), only matching checks are exported
4. Button label: "Export" with a download icon

### Import

Add an "Import" button in the checks list page toolbar.

**Behavior:**
1. Click "Import" → opens a file upload dialog
2. User selects a JSON file
3. The file is sent with `dryRun=true` first
4. A preview dialog shows:
   - Checks that will be **created** (new slugs)
   - Checks that will be **updated** (existing slugs)
   - Checks that have **errors** (with error messages)
5. User confirms → actual import runs (without `dryRun`)
6. Success toast with summary: "Imported 5 checks (3 created, 2 updated)"
7. Check list refreshes automatically

## Implementation

### Backend

**New files/changes:**
- `back/internal/handlers/checks/handler.go` — add `ExportChecks` and `ImportChecks` handler methods
- `back/internal/handlers/checks/service.go` — add `ExportChecks` and `ImportChecks` service methods
  - Export: fetch all checks (with optional filters), map to export format
  - Import: parse JSON, resolve groups by name, upsert each check by slug
- `back/internal/app/server.go` — register routes:
  - `GET /api/v1/orgs/:org/checks/export`
  - `POST /api/v1/orgs/:org/checks/import`

**Reuse existing logic:**
- `UpsertCheck` service method for the import upsert behavior
- `ListChecks` query logic for filtered exports
- Checker registry `Validate()` for config validation on import

### Dashboard

**Files to modify:**
- `apps/dash0/src/routes/orgs/$org/checks.index.tsx` — add Export/Import buttons to toolbar
- `apps/dash0/src/api/hooks.ts` — add `useExportChecks` and `useImportChecks` hooks

## Examples

### Export all checks
```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/checks/export' -o checks.json
```

### Export only HTTP checks
```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/checks/export?type=http' -o http-checks.json
```

### Dry-run import
```bash
curl -s -X POST \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @checks.json \
  'http://localhost:4000/api/v1/orgs/default/checks/import?dryRun=true' | jq '.'
```

### Import checks
```bash
curl -s -X POST \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d @checks.json \
  'http://localhost:4000/api/v1/orgs/default/checks/import' | jq '.'
```

### Migrate between instances
```bash
# Export from source
curl -s -H "Authorization: Bearer $SOURCE_TOKEN" \
  'https://source.solidping.com/api/v1/orgs/myorg/checks/export' -o checks.json

# Import to target
curl -s -X POST \
  -H "Authorization: Bearer $TARGET_TOKEN" \
  -H 'Content-Type: application/json' \
  -d @checks.json \
  'https://target.solidping.com/api/v1/orgs/myorg/checks/import'
```

## Test Plan

- Export all checks → verify JSON format, field completeness, no UIDs/timestamps
- Export with filters → verify only matching checks included
- Import into empty org → all checks created
- Import into org with existing checks → matching slugs updated, new slugs created
- Import with `dryRun=true` → verify no side effects, correct preview
- Import same file twice → idempotent (same result)
- Import with invalid checks → partial success, errors reported per check
- Import with unknown group names → groups auto-created
- Round-trip: export → import into different org → export again → compare (should be identical except `exportedAt` and `organization`)
