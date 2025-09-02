# Screenshot Implementation for Failed Checks

Take screenshots of websites when HTTP checks fail, similar to BetterStack.

---

## Technology Choice: Rod

After evaluating Go-based screenshot tools (see [docs/go_screenshot_tools_comparison.md](../../docs/go_screenshot_tools_comparison.md)), **Rod** (`github.com/go-rod/rod`) is the chosen library for the screenshot service:

- Best concurrency model (multiplexed events, no fixed-buffer bottleneck)
- Lower memory per tab (decode-on-demand, no DOM caching)
- Automatic zombie process cleanup (Leakless)
- Simpler API with dual `Must*`/error pattern
- MIT license
- `launcher.Manager` enables multi-browser management in a single container

---

## Architecture

### External Screenshot Service

The screenshot capture runs as a **separate containerized service** built with Rod. Workers communicate with it via HTTP.

**Configuration**: The screenshot service URL is provided to each worker via an environment variable:

```
SP_SCREENSHOT_SERVICE_URL=http://screenshot-service:8080
```

When this variable is not set, screenshot capture is disabled entirely (no-op).

### Flow

```
CheckWorker detects failure
  → check has "screenshot" enabled?
  → screenshot service URL configured?
    → POST screenshot service with URL + viewport settings
    → receive screenshot bytes
    → upload to S3: {organizationUid}/{checkUid}/{resultUid}.png
```

The screenshot is captured **inline during check execution** by calling the external service. This avoids the complexity of region-aware jobs while keeping the browser out of the worker process. The external service handles the heavy lifting (browser management, rendering), so the worker only makes an HTTP call + S3 upload.

### Screenshot Service API

The screenshot service exposes a simple HTTP API:

```
POST /screenshot
Content-Type: application/json

{
    "url": "https://example.com",
    "viewportWidth": 1920,
    "viewportHeight": 1080,
    "waitFor": 5
}

Response: image/png (screenshot bytes)
```

### Deployment

Each region deploys a screenshot service container alongside its workers. The worker's `SP_SCREENSHOT_SERVICE_URL` points to the local region's instance, ensuring screenshots are captured from the same network location as the check.

```yaml
# docker-compose example
screenshot-service:
  image: solidping/screenshot-service
  ports:
    - "8080:8080"
  shm_size: 2g
```

---

## Check Configuration

Screenshots are opt-in per HTTP check via a `screenshot` boolean property in the check config:

```json
{
    "url": "https://example.com",
    "method": "GET",
    "screenshot": true
}
```

Only HTTP/HTTPS checks support screenshots. The `screenshot` property defaults to `false`.

---

## Storage

Screenshots are stored in S3-compatible object storage (S3, MinIO, etc.).

### S3 Path Convention

```
screenshots/{organizationUid}/{checkUid}/{resultUid}.png
```

Example:
```
screenshots/org_abc123/chk_def456/res_ghi789.png
```

### Configuration

```
SP_S3_BUCKET=solidping-screenshots
SP_S3_ENDPOINT=https://s3.amazonaws.com    # or MinIO endpoint
SP_S3_REGION=us-east-1
SP_S3_ACCESS_KEY=...
SP_S3_SECRET_KEY=...
```

### Metadata in Database

The check result record stores a reference to the screenshot:

```sql
ALTER TABLE check_results ADD COLUMN screenshot_path TEXT;
```

When a screenshot is captured, `screenshot_path` is set to the S3 key (e.g., `screenshots/org_abc123/chk_def456/res_ghi789.png`). A `NULL` value means no screenshot was taken.

---

## Implementation Plan

1. **Phase 1: Screenshot Service**
   - Build standalone Rod-based HTTP screenshot service
   - Dockerfile with Rod's `ghcr.io/go-rod/rod` as base
   - Health check endpoint (`GET /health`)

2. **Phase 2: Worker Integration**
   - Add `SP_SCREENSHOT_SERVICE_URL` env var support
   - Add `screenshot` property to HTTP check config
   - On check failure: call screenshot service, upload to S3, store path in result
   - Handle timeouts and errors gracefully (screenshot failure must not affect check result)

3. **Phase 3: API & UI**
   - API endpoint: `GET /api/v1/orgs/$org/checks/$checkUid/results/$resultUid/screenshot`
   - Serve screenshot from S3 (proxy or signed URL)
   - Display screenshot in incident/result detail page

4. **Phase 4: Lifecycle**
   - Cleanup job for old screenshots based on retention policy
   - Organization-level storage quota

---

## Error Handling

- Screenshot service unavailable: log warning, skip screenshot, do not affect check result
- Screenshot timeout (e.g., page hangs): service returns error after 30s, worker skips screenshot
- S3 upload failure: log error, set `screenshot_path` to `NULL`, do not affect check result
- Screenshot capture is best-effort: it must never delay or alter the check result reporting
