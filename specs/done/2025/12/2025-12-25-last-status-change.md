# Last Status Change Feature

## Overview
Add the ability to track and retrieve when each check last changed its status (e.g., from UP to DOWN or DOWN to UP).

## API Changes

### GET /api/v1/orgs/$org/checks
Add optional query parameter `with=last_status_change` to include last status change information in the response.

**Example Request:**
```
GET /api/v1/orgs/$org/checks?with=last_status_change
```

**Response Format:**
Each check in the response should include:
- `lastStatusChange`: Object containing the time and status of when the check last transitioned between UP/DOWN states (null if no status change exists)
  - `time`: ISO 8601 timestamp of the status change
  - `status`: The status that the check changed to (UP or DOWN)

**Example Response:**
```json
{
  "data": [
    {
      "uid": "check_123",
      "name": "API Server",
      "status": "UP",
      "lastStatusChange": {
        "time": "2025-12-20T10:30:00Z",
        "status": "UP"
      },
      ...
    },
    {
      "uid": "check_456",
      "name": "Database",
      "status": "DOWN",
      "lastStatusChange": {
        "time": "2025-12-25T08:15:00Z",
        "status": "DOWN"
      },
      ...
    },
    {
      "uid": "check_789",
      "name": "New Check",
      "status": "UP",
      "lastStatusChange": null,
      ...
    }
  ]
}
```

## Definition
"Last status change" refers to the most recent time when a check transitioned from one status to another:
- UP → DOWN
- DOWN → UP

Status compaction events (merging consecutive status entries of the same type) do NOT count as status changes.

## Implementation Approach

### Data Source
Calculate from existing `check_results` table - no new database columns needed.

### Query Strategy
When `with=last_status_change` is requested:
1. For each check, query the most recent status transition in `check_results`
2. A status transition is identified by finding consecutive results where `status` changes
3. Use the `created_at` timestamp of the first result in the new status

### SQL Query Pattern (PostgreSQL)
```sql
-- Find last status change for a check
WITH status_changes AS (
  SELECT
    check_uid,
    created_at,
    status,
    LAG(status) OVER (PARTITION BY check_uid ORDER BY created_at) AS prev_status
  FROM check_results
  WHERE check_uid = $1
)
SELECT created_at, status
FROM status_changes
WHERE status IS DISTINCT FROM prev_status
ORDER BY created_at DESC
LIMIT 1;
```

### Backend Implementation Files
- `back/internal/dao/checks.go` - Add method to fetch last status change
- `back/internal/api/v1/checks/handler.go` - Parse `with` parameter and call DAO method
- `back/internal/api/v1/checks/models.go` - Add `LastStatusChange` field to response model

### Edge Cases
1. **No results yet**: Return `null` for the field
2. **Only one status (never changed)**: Return `null` for the field
3. **Check just created**: Return `null` for the field
4. **Multiple workers reporting**: Use the earliest `created_at` for the status transition

### Performance Considerations
- Only fetch last status change data when explicitly requested via `with` parameter
- Consider adding an index on `(check_uid, created_at DESC)` if not already present
- Limit lookback period if needed (e.g., last 90 days) to avoid scanning too much history

## Testing Requirements

### Unit Tests
- [ ] DAO method returns correct timestamp and status for status change
- [ ] DAO method returns null when no status change exists
- [ ] DAO method handles checks with only one status
- [ ] DAO method returns the correct status value (UP/DOWN) matching the transition

### Integration Tests
- [ ] API returns last status change when `with=last_status_change` is used
- [ ] API excludes fields when parameter is not provided
- [ ] Multiple checks with different status histories
- [ ] Response format matches specification

### Test Scenarios
1. Check with recent status change (UP → DOWN)
2. Check with old status change (DOWN → UP)
3. Check with no status changes (always UP)
4. Check with no results yet
5. Check with status compaction (verify compaction doesn't affect result)

## Use Case
Enable displaying uptime/downtime duration in the UI:
- "Up for 25 days"
- "Down for 3 hours"
- "Up for 2 minutes"
