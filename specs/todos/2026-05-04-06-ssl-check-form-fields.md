# SSL check form: typed fields & working examples

## Context

Backend SSL check support is solid:
- `server/internal/checkers/checkssl/config.go:19-35` — `SSLConfig{Host, Port, ThresholdDays, Timeout, ServerName}`.
- `server/internal/checkers/checkssl/samples.go:12-27` — sample configs `ssl-google-com` and `ssl-github-com`.
- Wired in the registry at `server/internal/checkers/registry/registry.go:78-79,155-156`.

The dash0 form (`web/dash0/src/components/shared/check-form.tsx`) lists "ssl" in its `checkTypes` array (line ~51) but, for SSL specifically, falls back to a generic JSON `config` editor (CodeMirror). The user reports that "examples don't work" — the most likely failure modes:
1. The template picker (shipped via `specs/done/2026/05/2026-05-02-12-check-form-template-picker.md`) inserts the sample's JSON, but the user has no typed UI to tweak it before saving, and the JSON keys may diverge from what `FromMap` expects (cf. spec 07 — same family of bug as the HTTP check's camelCase/snake_case).
2. There is no protocol-specific validation surfaced to the user, so a missing `host` is only caught on save with a generic error.

## Scope

**In scope:**
- A typed SSL section in `check-form.tsx`, mirroring the existing HTTP/TCP sections. Fields: Host (required), Port (default 443), Server Name / SNI (optional), Threshold Days (default 30), Timeout (default 10s).
- Wire the template picker to populate these fields directly when the user picks an SSL sample, instead of writing JSON to the config editor.
- Round-trip test: assert each sample in `samples.go` produces a config map that survives `FromMap → ToMap → FromMap`.
- A real-cert smoke test: an integration test that runs `ssl-google-com` against `google.com:443` and asserts `up`. Mark `t.Skip()` if no network in CI.
- Cross-check against spec 07 — if SSL config also reads snake_case keys but the form writes camelCase, fix it the same way.

**Out of scope:**
- OCSP / CT-log checks, cipher-suite assertions, custom CA bundles, mTLS client certs.
- Visual redesign of the form.

## Approach

### 1. Audit config-key casing

Read `server/internal/checkers/checkssl/config.go` `FromMap` and confirm whether it reads `thresholdDays` (camel) or `threshold_days` (snake). Whichever the spec-07 fix lands on becomes the convention; the form must match.

### 2. Typed form section

`web/dash0/src/components/shared/check-form.tsx`:

Add a `<SSLConfigFields>` block, rendered when `type === "ssl"`. Use the same `useFormContext` / `register` patterns as the HTTP block. Fields and validation:

| Field          | Form key     | Required | Default | Validation                         |
| -------------- | ------------ | -------- | ------- | ---------------------------------- |
| Host           | host         | yes      | —       | non-empty, no scheme, no path      |
| Port           | port         | no       | 443     | 1–65535                            |
| Server Name    | serverName   | no       | —       | optional FQDN                      |
| Threshold Days | thresholdDays | no      | 30      | 1–365                              |
| Timeout        | timeout      | no       | "10s"   | Go duration string                 |

On submit, the `config` JSON written to the API uses the same keys (post spec-07: camelCase).

### 3. Template picker

Where the template picker currently writes JSON to the CodeMirror editor for SSL, instead set the form values directly via `setValue("config.host", "google.com")`, etc. Branch by `type === "ssl"`. Same approach should work for HTTP/TCP if not already done.

### 4. Tests

`server/internal/checkers/checkssl/samples_test.go`:
```go
func TestSamples_RoundTrip(t *testing.T) {
    for _, s := range Samples() {
        cfg, err := FromMap(s.Config)
        require.NoError(t, err)
        // Re-serialize and parse again — same value
        again, err := FromMap(cfg.ToMap())
        require.NoError(t, err)
        require.Equal(t, cfg, again)
    }
}
```

E2E (`web/dash0/e2e/check-form-ssl.spec.ts`):
1. New check → pick "ssl" type → pick `ssl-google-com` sample → assert Host = `google.com`, Port = `443`, etc.
2. Save → wait for first run → assert status `up`.

## Verification

1. `make test` passes the new sample-roundtrip test.
2. `make dev`, create a new SSL check via the form, paste host `google.com`, save, watch first run produce `up`.
3. Picking the `ssl-github-com` template populates the typed form fields (visible immediately), not a raw JSON blob.
4. A check with `host=google.com` and `thresholdDays=1` produces a `warning` (or whatever the warn-state semantics are) — confirm the threshold logic round-trips end-to-end.
