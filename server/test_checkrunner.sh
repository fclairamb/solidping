#!/bin/bash
set -e

echo "=== Testing Check Runner ==="

# Clean up any previous test data
rm -f test.db test.log
rm -f solidping

# Build the application
echo "Building application..."
go build -o solidping .

# Initialize database
echo "Initializing database..."
rm -f test.db
sqlite3 test.db < ./internal/db/sqlite/migrations/20251207000001_initial.up.sql

# Create test data using sqlite3
echo "Creating test data..."
sqlite3 test.db <<EOF
-- Create organization
INSERT INTO organizations (uid, slug, created_at, updated_at)
VALUES ('test-org-uid', 'testorg', datetime('now'), datetime('now'));

-- Create check
INSERT INTO checks (uid, organization_uid, slug, name, type, config, enabled, created_at, updated_at)
VALUES (
  'test-check-uid',
  'test-org-uid',
  'http-check',
  'Test HTTP Check',
  'http',
  '{"url":"https://www.google.com","timeout":"5s"}',
  1,
  datetime('now'),
  datetime('now')
);

-- Create check job scheduled for now (should be picked up immediately)
INSERT INTO check_jobs (uid, organization_uid, check_uid, period, scheduled_at, updated_at)
VALUES (
  'test-job-uid',
  'test-org-uid',
  'test-check-uid',
  'PT1M',
  datetime('now', '-10 seconds'),
  datetime('now')
);

-- Verify data was created
SELECT 'Organizations:', COUNT(*) FROM organizations;
SELECT 'Checks:', COUNT(*) FROM checks;
SELECT 'CheckJobs:', COUNT(*) FROM check_jobs;
EOF

# Start server with check runners enabled (no job runners to avoid DB locking)
echo ""
echo "Starting server with 1 check runner (job runners disabled)..."
SP_DB_TYPE=sqlite SP_DB_DIR=. SP_SERVER_CHECK_RUNNERS=1 SP_SERVER_JOB_RUNNERS=0 ./solidping serve > test.log 2>&1 &
SERVER_PID=$!

echo "Server PID: $SERVER_PID"
echo "Waiting for server to start..."
sleep 5

# Check if server is running
if ! ps -p $SERVER_PID > /dev/null; then
  echo "ERROR: Server failed to start"
  cat test.log
  exit 1
fi

echo ""
echo "=== Server Log (first 30 lines) ==="
head -30 test.log

# Wait a bit for check runner to process the job
echo ""
echo "Waiting 10 seconds for check runner to process job..."
sleep 10

# Check results
echo ""
echo "=== Checking Results ==="
sqlite3 test.db <<EOF
.mode column
.headers on
SELECT 'Workers:' as info;
SELECT uid, slug, name, region, last_active_at FROM workers;

SELECT '' as '';
SELECT 'Check Jobs Status:' as info;
SELECT
  uid,
  scheduled_at,
  lease_worker_uid,
  lease_starts
FROM check_jobs;

SELECT '' as '';
SELECT 'Results Count:' as info;
SELECT COUNT(*) as result_count FROM results;

SELECT '' as '';
SELECT 'Latest Results:' as info;
SELECT
  started_at,
  status,
  duration_ms_avg,
  substr(output, 1, 100) as output_preview
FROM results
ORDER BY started_at DESC
LIMIT 3;
EOF

# Show more logs
echo ""
echo "=== Full Server Log ==="
cat test.log

# Cleanup
echo ""
echo "Stopping server..."
kill $SERVER_PID 2>/dev/null || true
sleep 1

echo ""
echo "=== Test Complete ==="
echo "Database file: test.db"
echo "Log file: test.log"
