---
model: sonnet
effort: medium
---

# `SP_SENTRY_TRACES_SAMPLE_RATE` and `SP_SENTRY_DEBUG` never reach the config

## Problem

The koanf env provider in [`config.go:1393`](../../server/internal/config/config.go)
collapses **every** underscore in an `SP_*` name into a dot:

```go
path := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(key, "SP_"), "_", "."))
```

`SentryConfig` ([`config.go:279`](../../server/internal/config/config.go)) declares:

```go
type SentryConfig struct {
	DSN              string  `koanf:"dsn"`
	Environment      string  `koanf:"environment"`
	TracesSampleRate float64 `koanf:"traces_sample_rate"` // 0.0 to 1.0 (default 0.1)
	Debug            bool    `koanf:"debug"`
}
```

So `SP_SENTRY_TRACES_SAMPLE_RATE` maps to `sentry.traces.sample.rate` and can
never reach `sentry.traces_sample_rate`. This is the same class of bug already
worked around by the hand-written readers for `rate_limiting`,
`shutdown_timeout`, `posthog` and `whatsapp` — see `applyPostHogEnv`
([`config.go:2022`](../../server/internal/config/config.go)) and the explanatory
comments at lines 1459, 1525, 1558, 1634 and 1959.

`SP_SENTRY_DEBUG` maps cleanly to `sentry.debug` and is *bound*, but it is
untested and shares the same code path, so it belongs in the same fix.

Two aggravating factors found while confirming this:

1. **There is no defaults entry for `SentryConfig` at all.** The defaults block
   at [`config.go:1328`](../../server/internal/config/config.go) sets `PostHog`
   but never `Sentry`, so `TracesSampleRate` falls to the Go zero value `0.0`.
   The struct comment and the shipped spec
   [`2026-03-28-03-sentry-integration.md`](../done/2026/03/2026-03-28-03-sentry-integration.md)
   both claim a default of `0.1`. **The code and the spec disagree, and neither
   states an intent** — today's `0.0` is an accident of Go zero values, not a
   decision.

2. **`SP_SENTRY_TRACES_SAMPLE_RATE` is not in the recognized-env-var set either.**
   `envNameForKoanfPath` ([`envvars.go:75`](../../server/internal/config/envvars.go))
   derives recognized names by reflection and *skips any koanf path segment
   containing an underscore*, so the name is unknown to `internal/envcheck` as
   well. There is no mention of Sentry anywhere in `envvars.go`.

`initSentry` ([`app/server.go:1712`](../../server/internal/app/server.go)) does
read `cfg.TracesSampleRate` and passes it to `sentry.Init`, so the plumbing is
otherwise complete — only the env binding is missing.

**Why this matters now:** the Sentry project `webingenia/solidping-server` was
created on 2026-08-17 and `SP_SENTRY_DSN` + `SP_SENTRY_ENVIRONMENT` are wired
into `k8s/solidping/overlays/{dev,prod}/secret.yaml` in the `k8xp` repo. Those
two names *are* reflection-reachable (single-word segments) and work correctly.
The sampling rate is the one knob an operator will reach for first, and it is
the one that does nothing.

## Proposal

### 1. Add `applySentryEnv`

Follow the `applyPostHogEnv` pattern exactly, including its doc comment style
explaining *why* the reader exists:

```go
// applySentryEnv reads SP_SENTRY_* into cfg. sentry.dsn / sentry.environment /
// sentry.debug are koanf-reachable (single-word segments) and already bound by
// the env provider, but sentry.traces_sample_rate has a snake_case segment that
// koanf's underscore→dot collapsing can never reach, so it is read by hand
// here. Keep in sync with manualReaderPlatformEnvVars.
func applySentryEnv(cfg *SentryConfig) { … }
```

- Read `SP_SENTRY_TRACES_SAMPLE_RATE` as a float. **Reject out-of-range values**
  rather than clamping silently: a rate outside `[0.0, 1.0]` is an operator
  error, and Sentry treats `>1` as `1` without complaint. Match whatever the
  neighbouring readers do for malformed numeric input (parse failure → leave the
  existing value, or surface an error — follow the established convention rather
  than inventing a third one).
- Read `SP_SENTRY_DEBUG` as a bool.
- **Do not touch `SP_SENTRY_DSN` or `SP_SENTRY_ENVIRONMENT`** — they already bind
  through the env provider, and re-reading them by hand would change precedence.
- Wire the call in next to the other `apply*Env` calls, near
  [`config.go:1473`](../../server/internal/config/config.go).

### 2. Register the names in `envvars.go`

Add `SP_SENTRY_TRACES_SAMPLE_RATE` and `SP_SENTRY_DEBUG` to
`manualReaderPlatformEnvVars()` ([`envvars.go:185`](../../server/internal/config/envvars.go)).
Without this, `internal/envcheck` reports a freshly-supported variable as
unknown and suggests a "did you mean". `TestManualReaderEnvVarsBind` spot-checks
that listed names actually bind, so this is load-bearing, not bookkeeping.

`SP_SENTRY_DEBUG` is reflection-derived already; adding it to the manual list is
only correct if the reader genuinely reads it — otherwise leave it out. Decide
based on what the implementation ends up doing.

### 3. Settle the default deliberately

Pick one and make it explicit in the defaults block at
[`config.go:1328`](../../server/internal/config/config.go) — do not leave it as
a Go zero value either way.

**Recommendation: keep `0.0` and correct the documentation.** SolidPing already
has OpenTelemetry tracing behind `SP_OTEL_ENABLED`, and the `/docs` observability
page presents the three toggles as independent. Paying Sentry's transaction
quota for a second, thinner trace stream is duplicate spend for a self-hostable
product where the operator may have no Sentry plan at all. Errors and panics —
the thing Sentry is genuinely better at — are captured at 100% regardless of
this setting.

If `0.0` is chosen:
- set it explicitly in the defaults block with a comment naming the reason,
- fix the `// 0.0 to 1.0 (default 0.1)` comment on the struct field,
- and update the claim in
  [`2026-03-28-03-sentry-integration.md`](../done/2026/03/2026-03-28-03-sentry-integration.md).
  That spec is in `done/`, so amend it with a dated note rather than silently
  rewriting history.

Also check whether `web/docs/docs/features/observability.md` documents a Sentry
sample rate; if it does, it needs the same correction.

### 4. Tests

Table-driven, in `internal/config/`, covering both variables. The test must
**prove the value is applied**, not merely that a default survives — a test that
asserts `cfg.Sentry.TracesSampleRate == 0.0` after setting the env var to `0.0`
passes today with the bug fully present. Positive controls required:

| Case | Env | Expect |
|---|---|---|
| rate applied | `SP_SENTRY_TRACES_SAMPLE_RATE=0.25` | `0.25` — fails before the fix |
| rate unset | (unset) | the chosen default |
| rate out of range | `=1.5`, `=-0.1` | rejected per the convention chosen above |
| rate malformed | `=abc` | rejected / ignored per convention |
| debug applied | `SP_SENTRY_DEBUG=true` | `true` |
| DSN unaffected | `SP_SENTRY_DSN=https://…` | still binds — guards against a regression in the reader |

Add a case to whichever existing test enumerates recognized env vars so the two
new names are covered there too.

## Out of scope

The broader question of whether the underscore-collapsing env provider should be
replaced with something that handles snake_case keys natively. Six hand-written
readers now exist to paper over it. That is worth its own spec; this one only
stops Sentry from being the seventh silent case.
