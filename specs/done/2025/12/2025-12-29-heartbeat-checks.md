# Heartbeat Checks

## Overview
Heartbeat checks are a monitoring pattern where instead of actively polling a service, the monitored service reports its own status to the monitoring system.

## Goal
Allow clients to call the SolidPing API to notify that a check has succeeded or failed. This is useful for:
- Monitoring cron jobs or scheduled tasks
- Tracking completion of batch processes
- Monitoring systems behind firewalls that cannot be actively probed
- "Heartbeat" monitoring where silence indicates failure

## Implementation Concept
The check verification happens when a client accesses a specific URL. SolidPing records the access as a successful check execution.

## Key Differences from Active Checks
- **Active checks**: SolidPing reaches out to test a service
- **Heartbeat checks**: The service reaches out to SolidPing to report status

## Expected Behavior
1. User creates a heartbeat check and receives a unique URL/token
2. Client service calls this URL at regular intervals or after task completion
3. If SolidPing doesn't receive the notification within the expected time window, an alert is triggered
4. Optionally, clients can report failure status explicitly

## Example Use Cases
- A backup script calls the URL after successful completion
- A data processing job notifies SolidPing every hour it's still running
- A scheduled task reports success/failure after each execution

## Implementation

### Check Configuration
Extend the existing check types to include `heartbeat`:
- `check_type`: Add new value `heartbeat` (alongside existing `http`, `tcp`, `icmp`, etc.)
- `interval`: Expected time between pings (e.g., 1 hour, 1 day)
- `grace_period`: Additional time to wait before recording failure (e.g., 10 minutes)
- `token_uid`: Unique token for authenticating heartbeat pings (separate from check_uid for security)
- Uses existing `scheduled_at` field to track when next ping is expected

### Database Schema Changes
No schema changes required:
- `token` (UUID v4) is stored in the check's `config` field
- Uses existing `scheduled_at` field to track when next ping is expected
- Check results store timeout/failure status as usual

### API Endpoints

#### Public Endpoint (No Auth Required)
```
POST /api/v1/orgs/{org}/heartbeat/{check_uid}/{token_uid}
GET  /api/v1/orgs/{org}/heartbeat/{check_uid}/{token_uid}  -- Simple heartbeat, always success
```

**POST Body** (optional):
```json
{
  "status": "success|failure",  -- Optional, defaults to "success"
  "output": {"message": "Backup completed"},  -- Optional metadata
  "metrics": {"duration": 3600}  -- Optional, execution duration in seconds
}
```

**Response**:
```json
{
  "status": "ok",
  "check_uid": "<result_uid>",
  "recorded_at": "2025-12-18T10:30:00Z"
}
```

#### Management Endpoints (Authenticated)
```
GET    /api/v1/orgs/{org}/checks/{check_uid}/token
POST   /api/v1/orgs/{org}/checks/{check_uid}/token/regenerate
```

### Alert Logic
The heartbeat check monitoring runs alongside the active check scheduler:

#### On Ping Receipt (POST/GET /api/v1/orgs/{org}/heartbeat/{check_uid}/{token_uid})
1. Validate that the check belongs to the org and token_uid matches
2. Create a success/failure result based on the `status` field (defaults to "success")
3. Set `scheduled_at = NOW() + interval` (expecting next ping in `interval` time)
4. Store optional metadata (output, metrics) in check results
5. If previous result was timeout/failure, this triggers a recovery alert

#### Periodic Heartbeat Check Monitor (runs every minute)
For each heartbeat check where `scheduled_at < NOW()`:

1. **Check last result**:
   - If last result is **NOT timeout**:
     - Record a **"timeout"** result
     - Set `scheduled_at = scheduled_at + grace_period`
     - Do NOT trigger alert yet (grace period)

   - If last result **IS timeout**:
     - Record a **"failure"** result
     - Set `scheduled_at = NULL` (stop scheduling)
     - Trigger failure alert

2. **Dormant state**: Once `scheduled_at = NULL`, the check is dormant and won't be checked again until a heartbeat ping is received, which will set `scheduled_at = NOW() + interval` and resume monitoring

### Security Considerations
- **Token-based access**: No authentication required for ping endpoint, security through separate `token_uid`
- **Two-factor URL**: Both `check_uid` and `token_uid` must match for access
- **Token regeneration**: Allow users to regenerate `token_uid` if compromised without changing `check_uid`
- **Rate limiting**: Limit pings to prevent abuse (e.g., max 10 pings per minute per token_uid)
- **HTTPS only**: Enforce HTTPS in production to prevent token leakage in logs/referrers
- **Token format**: UUID v4 for cryptographically secure random tokens

### Integration with Existing System
- Reuse existing check results storage for heartbeat check events
- Reuse alert/notification system (email, webhook, etc.)
- Display heartbeat checks in the same UI as active checks
- Check history shows both active probes and heartbeat pings
- Statistics: uptime calculation based on expected vs actual ping frequency

### UI Changes
- **Check Creation**: Add "Heartbeat" as a check type option, generate `token_uid` (UUID v4) automatically
- **Check Details**: Display the full heartbeat check URL with copy button (e.g., `https://solidping.example.com/api/v1/orgs/demo/heartbeat/chk_abc123/550e8400-e29b-41d4-a716-446655440000`)
- **Token Management**: Show "Regenerate Token" button with confirmation dialog
- **Results View**: Show ping metadata (output, metrics) when available, distinguish between timeout and failure results
- **Expected Schedule**: Display when next ping is expected (`scheduled_at`) and grace period
- **Dormant State**: Indicate when a check is dormant (`scheduled_at = NULL`) and waiting for a ping

### Example Curl Usage
```bash
# Simple heartbeat (GET or POST with no body)
curl https://solidping.example.com/api/v1/orgs/demo/heartbeat/chk_abc123/550e8400-e29b-41d4-a716-446655440000

# Report success with metadata
curl -X POST https://solidping.example.com/api/v1/orgs/demo/heartbeat/chk_abc123/550e8400-e29b-41d4-a716-446655440000 \
  -H "Content-Type: application/json" \
  -d '{"status": "success", "output": {"message": "Backup completed"}, "metrics": {"duration": 3600}}'

# Report failure
curl -X POST https://solidping.example.com/api/v1/orgs/demo/heartbeat/chk_abc123/550e8400-e29b-41d4-a716-446655440000 \
  -H "Content-Type: application/json" \
  -d '{"status": "failure", "output": {"message": "Backup failed: disk full"}}'
```

### Migration Path
1. Add `heartbeat` to check_type enum/validation
2. Deploy public ping API endpoints (`/api/v1/orgs/{org}/heartbeat/{check_uid}/{token_uid}`)
3. Deploy management endpoints for token viewing and regeneration
4. Update scheduler to monitor heartbeat checks (handle timeout → failure logic)
5. Update UI to support heartbeat check creation, token display, and dormant state
6. No database migrations required, no impact on existing active checks
