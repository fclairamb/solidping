# Claude API: add status-page HTTP sample

## Context

The default HTTP samples (`server/internal/checkers/checkhttp/samples.go`) ship two entries: a
local fake-API probe and Cloudflare DNS. There is no AI/LLM provider in the list. Anthropic's
Claude API is one of the more volatile widely-used APIs — being able to show it monitored out of
the box is a strong product-demo story and useful for any operator who relies on it.

This spec adds one default sample and is intentionally narrow. The deeper API probe (using the
free `messages/count_tokens` endpoint with an `x-api-key` header) requires secret header support
first; that is covered in
[`2026-05-18-07-secret-http-headers.md`](2026-05-18-07-secret-http-headers.md).

## Honest opinion

The status-page JSON is the only sane starting point. A `POST /v1/messages` call requires an API
key (no key → 401) and burns tokens (money) on every probe — completely wrong for a default
sample. The public status API at `https://status.claude.com/api/v2/status.json` needs no auth,
costs nothing, and returns a structured `status.indicator` field the existing JSON-path assertion
engine can assert on.

Caveats that belong in the sample description:
- The status page reflects Anthropic's own monitoring, which lags real outages. It won't catch
  latency spikes or partial degradation from your network.
- For a real API reachability probe, see the follow-up spec that introduces the count_tokens
  sample once secret headers land.

Longer-term, this is one entry in what should become a curated "well-known service" catalog
(Claude, OpenAI, Gemini, Stripe, …) the user opts into from the UI rather than auto-seeded.
Calling that out as future work; not doing it here.

## Goal

- Add one default HTTP sample: `http-claude-api` probing `https://status.claude.com/api/v2/status.json`.
- Assert `$.status.indicator` equals `"none"` (the statuspage.io v2 value for "all systems operational").
- No code changes outside `checkhttp/samples.go` and a test.

## Non-goals

- Backfilling existing orgs (the `samples.loaded=true` parameter already gates re-seeding;
  no migration logic).
- Demo or test-mode variants of this sample.
- A `checkclaude` package — `checkhttp` covers everything needed.
- Other AI provider samples (follow-up).

## Design

### Sample entry

Add to the default branch of `GetSampleConfigs` in
`server/internal/checkers/checkhttp/samples.go`:

```go
{
    Name:   "Claude API",
    Slug:   "http-claude-api",
    Period: time.Minute,
    Config: (&HTTPConfig{
        URL:                 "https://status.claude.com/api/v2/status.json",
        Method:              methodGET,
        ExpectedStatusCodes: []string{"2XX"},
        JSONPathAssertions: &AssertionNode{
            Type:     NodeTypeAssertion,
            Path:     "$.status.indicator",
            Operator: "eq",
            Value:    "none",
        },
    }).GetConfig(),
},
```

`ExpectedStatusCodes: ["2XX"]` is used instead of the legacy `ExpectedStatus: 200` to avoid
brittleness if Cloudflare CDN injects a 304 on cache hits.

The `AssertionNode` using `"eq"` operator against `"none"` is the most stable signal in the
statuspage.io v2 schema. Values are `"none"` (operational), `"minor"`, `"major"`, `"critical"`,
`"maintenance"` — any non-none means degraded.

### Test

Add a test case (or extend existing ones) in
`server/internal/checkers/checkhttp/checker_test.go` (or a new `samples_test.go`) that calls
`(*HTTPChecker).GetSampleConfigs(nil)` and, for the `http-claude-api` entry, calls
`config.Validate()` to confirm the JSON-path assertion compiles correctly:

```go
for _, spec := range checker.GetSampleConfigs(nil) {
    cfg := &HTTPConfig{}
    require.NoError(t, cfg.FromMap(spec.Config))
    require.NoError(t, cfg.Validate())
}
```

This catches operator typos or invalid JSONPath expressions at test time.

## Files to change

### Modified files
- `server/internal/checkers/checkhttp/samples.go` — add sample entry

### New or modified tests
- `server/internal/checkers/checkhttp/checker_test.go` — add/extend "all default samples validate" test case

## Verification

```bash
make lint && make test
```

Smoke-test the live URL:
```bash
curl -sL https://status.claude.com/api/v2/status.json | jq '.status.indicator'
# expect: "none"
```

Fresh-install flow:
1. `rm -f data/solidping.db && make dev`
2. Log in as `admin@solidping.com` / `solidpass`
3. Navigate to Checks — confirm "Claude API" appears and reports `Up`

## Risk log

| Risk | Mitigation |
|---|---|
| status.claude.com URL changes (Anthropic rebrand) | URL redirects from status.anthropic.com today; can be updated in samples.go in one line if it moves again |
| `status.indicator` field renamed by statuspage.io | statuspage.io v2 API has been stable for years; add a comment citing the schema version |
| Sample silently fails for existing orgs | Intentional — `samples.loaded=true` prevents re-seeding; operator can add the check manually or via API |
| Future: singling out Claude while ignoring OpenAI/Gemini | Noted as a known smell; the real fix is a UI-opt-in catalog — flagged as follow-up |
