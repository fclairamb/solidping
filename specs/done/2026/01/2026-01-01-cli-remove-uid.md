# Remove UID Column from CLI `checks list` Output

## Problem

The `./solidping client checks list` command currently displays a UID column containing full UUIDs (e.g., `63d49e55-97e3-4e8c-b7ab-c862de7a43f3`). This creates several issues:

1. **Terminal clutter**: UUIDs are 36 characters long and dominate the table width
2. **Redundancy**: Users reference checks by slug (human-readable), not UID
3. **Poor readability**: The important columns (name, status, type) get compressed

## Current Behavior

```
UID                                   SLUG      NAME       TYPE   PERIOD    ENABLED  STATUS
63d49e55-97e3-4e8c-b7ab-c862de7a43f3  google    Google     http   00:01:00  yes      up (2h)
a1b2c3d4-e5f6-7890-abcd-ef1234567890  cloudflr  Cloudflare http   00:01:00  yes      up (1d)
```

## Desired Behavior

```
SLUG      NAME       TYPE   PERIOD    ENABLED  STATUS
google    Google     http   00:01:00  yes      up (2h)
cloudflr  Cloudflare http   00:01:00  yes      up (1d)
```

## Scope

- Remove the UID column from `checks list` default table output
- Remove the UID column from `checks list --with-last-result` table output
- Keep UID in JSON output (`--json` flag) for programmatic access
- The `results list` and other commands are **not affected** by this change

## Implementation

File: `back/pkg/cli/checks.go`

1. Remove `"UID"` from table headers (lines 146, 148)
2. Remove `uid` from table rows (lines 203, 205)
3. Remove the unused `uid` variable assignment (lines 187-190)
