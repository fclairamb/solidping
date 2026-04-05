# Test HTTP Checks Specification

When running in test mode (`SP_RUNMODE=test`), the startup job creates HTTP checks that target the built-in fake API endpoint (`/api/v1/fake`). These checks provide realistic monitoring scenarios without requiring external services.

## Configuration

The base URL for all fake API checks is derived from the `SP_SERVER_BASE_URL` environment variable, defaulting to `http://localhost:4000`.

## Fake API behavior

The fake API cycles between "up" and "down" states based on the `period` parameter:

```
isUp = (unix_timestamp / period) % 2 == 0
```

Within each `period` seconds, the endpoint is up for the first half and down for the second half.

## Check definitions

| Slug | Name | Period | Fake API Params | Behavior |
|------|------|--------|-----------------|----------|
| `http-fake-stable` | Fake API (Stable) | 10s | `period=86400` | Effectively always up (24h cycle) |
| `http-fake-flaky` | Fake API (Flaky) | 15s | `period=120` | Up 60s, down 60s |
| `http-fake-unstable` | Fake API (Unstable) | 15s | `period=40` | Up 20s, down 20s |
| `http-fake-slow` | Fake API (Slow) | 20s | `period=86400&delay=2000` | Always up but with 2s response delay |
| `http-fake-503` | Fake API (503 errors) | 15s | `period=60&statusDown=503` | Returns 503 (instead of default 500) when down |

All checks expect HTTP 200 and use the GET method.

## Failure patterns

- **Stable**: Should never fail during normal testing. Useful as a baseline.
- **Flaky**: Fails roughly 50% of the time in 2-minute cycles. Triggers incidents and recoveries regularly.
- **Unstable**: Fails roughly 50% of the time in short 40-second cycles. Produces frequent status changes.
- **Slow**: Always succeeds but with high latency. Useful for testing duration metrics and timeout behavior.
- **503 errors**: Same cycling as flaky but returns 503 instead of 500, useful for testing status code handling.
