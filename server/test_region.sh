#!/bin/bash
set -e

echo "Testing region configuration..."

# Clean up
rm -f test.db
pkill solidping 2>/dev/null || true
sleep 1

# Apply migrations
sqlite3 test.db < ./internal/db/sqlite/migrations/20251207000001_initial.up.sql

# Start server with region set
echo "Starting server with SP_REGION=us-east-1..."
SP_DB_TYPE=sqlite \
SP_DB_DIR=. \
SP_SERVER_CHECK_RUNNERS=1 \
SP_SERVER_JOB_RUNNERS=0 \
SP_REGION=us-east-1 \
./solidping serve > test_region.log 2>&1 &

SERVER_PID=$!
sleep 3

# Check if server started
if ! ps -p $SERVER_PID > /dev/null; then
  echo "ERROR: Server failed to start"
  cat test_region.log
  exit 1
fi

# Wait for worker registration
sleep 3

# Check worker region
echo ""
echo "Checking worker region..."
sqlite3 test.db <<SQL
.mode column
.headers on
SELECT slug, region FROM workers;
SQL

# Cleanup
kill $SERVER_PID 2>/dev/null || true
sleep 1

echo ""
echo "Test complete!"
