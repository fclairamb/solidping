# Fix badge response-time unit (always shows "s" instead of "ms")

## Bug

The `response-time` badge component always renders the value with an `s` suffix
(e.g. `63.1s`) even for sub-second response times that should display as
milliseconds (e.g. `63ms`).

Screenshot: a Docker PostgreSQL check with a ~63 ms round-trip shows `up 63.1s`.

## Root cause

`server/internal/handlers/badges/service.go` — `formatResponseTime` (line ~272):

```go
mean := sum / float64(count)

if mean < 1.0 {
    return fmt.Sprintf("%dms", int(mean*1000))
}
return fmt.Sprintf("%.1fs", mean)
```

The `Duration` field on `models.Result` is stored in **milliseconds** (set in
`server/internal/checkworker/worker.go` as
`float32(checkResult.Duration.Seconds() * 1000)`).

The function incorrectly treats the value as seconds, so a 63 ms response time
(`mean = 63.0`) fails the `< 1.0` guard and is formatted as `63.0s`.

## Fix

Update `formatResponseTime` to treat `mean` as milliseconds:

```go
// mean is in milliseconds
if mean < 1000 {
    return fmt.Sprintf("%dms", int(math.Round(mean)))
}
return fmt.Sprintf("%.1fs", mean/1000)
```

File: `server/internal/handlers/badges/service.go`

## Acceptance criteria

- [ ] A check with a ~63 ms response time renders `63ms` on the badge, not `63.1s`.
- [ ] A check with a ~1.5 s response time renders `1.5s` on the badge.
- [ ] Existing `formatResponseTime` unit tests in
      `server/internal/handlers/badges/service_test.go` are updated (or added)
      to cover both `ms` and `s` cases.
- [ ] No change to badge shape, color, or other components.
