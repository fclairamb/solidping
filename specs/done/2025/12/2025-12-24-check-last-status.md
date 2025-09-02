# Feature: Include Last Check Result in Checks Listing

## Overview
The checks listing endpoint should optionally include the last execution result for each check.

## API Endpoint
`GET /api/v1/orgs/$org/checks?with=last_result`

## Query Parameters
- `with` (optional, string): Comma-separated list of additional data to include
  - `last_result`: Include the most recent check result for each check

## Response Structure
When `with=last_result` is specified, each check object should include:

```json
{
  "data": [
    {
      "uid": "check_123",
      "name": "Example Check",
      "slug": "example-check",
      // ... other check fields ...
      "last_result": {
        "uid": "result_789",
        "status": "success",      // or "failure", "timeout", etc.
        "timestamp": "2025-12-24T10:30:00Z",
        "output": {
          "message": "Response time: 45ms",
          "details": "All health checks passed"
        },
        "metrics": {
          "response_time_ms": 45,
          "status_code": 200
        }
      }
    }
  ]
}
```

## Implementation Details

### Backend Changes

#### 1. Query Parameter Parsing
- In the checks listing handler, parse the `with` query parameter
- Check if it contains `last_result`
- Example: `with=last_result` or `with=last_result,other_field`

#### 2. Database Query
- When `with=last_result` is present, modify the query to include a LEFT JOIN with the results table
- Query should fetch the most recent result per check based on `timestamp` (or `created_at`)
- Use a subquery or window function to get only the latest result per check:

```sql
SELECT c.*, r.uid as last_result_uid, r.status, r.timestamp, r.output, r.metrics
FROM checks c
LEFT JOIN LATERAL (
    SELECT uid, status, timestamp, output, metrics
    FROM results
    WHERE results.check_uid = c.uid
    ORDER BY timestamp DESC
    LIMIT 1
) r ON true
WHERE c.org_uid = $1
```

#### 3. Response Serialization
- If `last_result` data is present in the query result, populate the `last_result` field
- If no result exists (NULL from LEFT JOIN), set `last_result` to `null`
- Ensure JSONB fields (`output`, `metrics`) are properly unmarshaled from the database

#### 4. Struct Updates
- Update the check response struct to include an optional `LastResult` field with `json:",omitempty"` tag
- Create or use existing result struct with fields: `UID`, `Status`, `Timestamp`, `Output` (jsonb), `Metrics` (jsonb)

### Performance Considerations
- The LATERAL join should be efficient with proper indexes on `results.check_uid` and `results.timestamp`
- Consider adding a composite index: `CREATE INDEX idx_results_check_timestamp ON results(check_uid, timestamp DESC)`
- For organizations with many checks, this should still perform well as it's a single query

### Edge Cases
- If a check has never been executed, `last_result` should be `null`
- If the `with` parameter contains invalid values, ignore them (only process known values)
- Empty `output` or `metrics` JSONB fields should be returned as empty objects `{}` not `null`

### Testing
- Test with `with=last_result` parameter present
- Test without the parameter (should not include `last_result` field)
- Test with checks that have no results (should have `last_result: null`)
- Test with checks that have multiple results (should only return the latest)
- Test JSONB field serialization for `output` and `metrics`
