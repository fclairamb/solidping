---
model: sonnet
effort: low
---

# TOTP and recovery-code verification is unthrottled

## Problem

`Verify2FA` and recovery-code validation (`handlers/auth/service.go:4297,
4371`) have no per-user attempt counter. The flow gates on a 5-minute
`purpose=2fa` temp token (`service.go:4133`), but the only other limit is
the global per-IP token bucket (1800 req/min default) — roughly 9000 guesses
per temp token against ~30 valid TOTP slots, accumulating across sessions
with zero lockout. Recovery codes are even fewer valid values. Password
login got no per-account throttle either (tracked separately), but 2FA is
the layer whose entire value is being hard to guess — it deserves its own
limiter.

## Proposal

1. Reuse the established per-user limiter pattern (the one protecting
   change-password at 10/15 min per user, `service.go:2316-2318`):
   - **5 failed TOTP attempts per 15 min per user**, then
     `429 VALIDATION_ERROR`-shaped `TOO_MANY_ATTEMPTS` (match the error code
     used by the other user-scoped limiters) with a generic message that
     does not reveal the counter state.
   - The temp token's own 5-minute expiry already bounds each window; this
     limiter additionally spans temp tokens, so re-requesting a temp token
     does not reset the counter.
2. Apply to both code paths: TOTP verify and recovery-code verify (recovery
   codes additionally stay single-use as today).
3. Successful verification resets the user's failed counter.
4. Keep the response for a wrong code and for a throttled request distinct
   enough to be usable (429 vs 401) — 2FA throttling is user-facing and the
   legit user must know to wait; this differs from login's uniform-401
   anti-enumeration choice because the user already proved their password.
5. Docs/changelog: no API shape change beyond the new 429 case.

## Tests

- Both DBs: 5 wrong TOTP codes in a row → 5th answer is the generic 401,
  6th is 429; correct code after a failure within budget → 200 and counter
  reset; after throttling, even the correct code gives 429 until the window
  passes (inject time via the existing clock seam if there is one, else a
  short window in tests).
- Recovery codes share the counter with TOTP (same user budget).
- Temp-token refresh does not reset the counter (two temp tokens, 10 total
  wrong codes → throttled).
- Other users are unaffected (per-user scoping, not per-IP).