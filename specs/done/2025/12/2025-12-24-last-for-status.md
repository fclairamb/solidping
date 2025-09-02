# Last Result Per Status Optimization

## Problem
Currently, finding the last result for each status (success, failure, timeout) for a check requires:
- Scanning all results for that check
- Filtering by status
- Ordering by timestamp
- Taking the first result

This can be slow for checks with many historical results.

## Solution
Add a `last_for_status` boolean field to the `results` table that marks the most recent result for each status per check.

## How It Works
- Each check can have multiple "last" results - one per status type
- Only ONE result per check per status can have `last_for_status = true`
- Example: For check `chk_123`:
  - Result #100 (success) has `last_for_status = true` (most recent success)
  - Result #98 (failure) has `last_for_status = true` (most recent failure)
  - Result #95 (timeout) has `last_for_status = true` (most recent timeout)
  - All other results have `last_for_status = null`

## Benefits
- Instant lookup: `SELECT * FROM results WHERE check_uid = ? AND status = ? AND last_for_status = true`
- No need to scan all results or use ORDER BY
- Indexed for fast access

## Database Schema
```sql
ALTER TABLE results ADD COLUMN last_for_status BOOLEAN;

-- Create partial index for fast lookup (only index true values)
-- NULL values are not indexed, keeping the index small and efficient
CREATE INDEX idx_results_last_for_status
ON results(check_uid, status)
WHERE last_for_status = true;
```

## Implementation Logic
When inserting a new result:

1. **Before insert**: Clear previous "last" for this check+status combination
   ```sql
   UPDATE results
   SET last_for_status = NULL
   WHERE check_uid = ? AND status = ? AND last_for_status = true;
   ```

2. **Insert**: Add new result with `last_for_status = true`
   ```sql
   INSERT INTO results (check_uid, status, last_for_status, ...)
   VALUES (?, ?, true, ...);
   ```

3. **Transaction**: Both operations should be in the same transaction for consistency

## Field Values
- `last_for_status = true`: This is the last result for this check+status
- `last_for_status = null`: This is NOT the last result (default)

Benefits of using NULL:
- Smaller index (NULL values are not indexed in the partial index)
- Clearer semantics (NULL = "not applicable")
- More efficient storage

## Use Cases
- **Check status display**: Show when check last succeeded, last failed, last timed out
- **Alert logic**: Determine if state changed (e.g., was last result success and now failure?)
- **Dashboard**: Quick overview of most recent status per check type
- **Recovery detection**: Check if last result before current was failure (recovery scenario)

## Migration
```sql
-- Add column (defaults to NULL, which is what we want for non-last results)
ALTER TABLE results ADD COLUMN last_for_status BOOLEAN;

-- Populate existing data (mark most recent result per check+status)
WITH ranked_results AS (
  SELECT
    uid,
    ROW_NUMBER() OVER (PARTITION BY check_uid, status ORDER BY created_at DESC) as rn
  FROM results
)
UPDATE results
SET last_for_status = true
WHERE uid IN (
  SELECT uid FROM ranked_results WHERE rn = 1
);

-- Create partial index (only indexes true values, NULL values are excluded)
CREATE INDEX idx_results_last_for_status
ON results(check_uid, status)
WHERE last_for_status = true;
```
