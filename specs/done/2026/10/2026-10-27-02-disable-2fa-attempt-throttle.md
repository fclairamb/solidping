---
model: opus
effort: low
---

# `DELETE /auth/2fa` checks the TOTP code with no attempt budget

## Problem

Found by the security scan of 2026-10-27 (finding W1).

**Severity: low.** Exploiting it takes a stolen session access token or an unscoped PAT of a user who has 2FA enabled, and hours of sustained requests. It lets that holder remove the victim's second factor for good.

- Login 2FA is throttled. `Verify2FA` and `Recovery2FA` spend the per-user budget before looking at the code: 5 attempts per 15 minutes (`server/internal/handlers/auth/service.go:4605-4606`, `:4769`, `:4812`). The comment at `:4764-4767` gives the reason: the value of 2FA is that the code is hard to guess.
- `Disable2FA` (`service.go:4866-4893`) calls `totp.Validate` on the submitted code with no counter at all. The route is `rootAuthProtected.DELETE("/2fa")` (`server/internal/app/server.go:845`), behind `RequireAuth` only. The handler (`handler.go:1448-1472`) adds nothing either.
- The only limit left is the global per-IP limiter: 1800 requests per minute by default (`server/internal/config/config.go:1899`). `totp.Validate` accepts the previous, current and next 30-second step, so a guess hits about 3 of the 10^6 codes. At the global cap, a single IP finds a valid code in a few hours on average, and spreading the requests over several IPs shortens that.
- On success the secret and the recovery codes are wiped (`service.go:4884-4892`). From then on the account's password alone is enough to sign in.

**Why it matters:** 2FA is meant to survive a partial compromise. Someone holding only a short-lived session, or a PAT leaked from CI, can turn it into a lasting downgrade. After that, a phished or reused password is enough for an interactive sign-in. The login path explicitly protects the second factor against guessing, and this endpoint quietly does not.

`Confirm2FA` (`service.go:4698-4720`) is not affected: it checks a secret that `Setup2FA` has just returned to the same caller, so guessing it gains nothing.

## Proposal

1. In `Disable2FA` (`server/internal/handlers/auth/service.go:4866`), call `bumpTwoFAAttemptCounter(ctx, userUID)` before reading the user. Do it the same way as `Verify2FA` (`:4769-4776`): spend before comparing, and return `ErrRateLimited` when the budget is exhausted.
   - Share the login budget (same key): disabling must not open a second allowance next to the login one.
   - Call `resetTwoFAAttemptCounter` after a successful disable, like `Verify2FA` does at `:4795`.
2. `handle2FAError` already maps `ErrRateLimited` to 429 `RATE_LIMITED` (`handler.go:1477-1478`). Check that dash0's disable dialog shows that error as a message instead of a generic failure (`web/dash0/src/contexts/AuthContext.tsx`, `web/dash0/src/components/security/`).
3. Keep the change scoped. Do not add a current-password requirement: federated- and passkey-only users have no password.

## Tests

- `server/internal/handlers/auth/two_fa_attempt_throttle_test.go`, as a new case in `runTwoFAAttemptThrottleSuite` so it runs on both engines:
  - **Regression (fails before the fix):** for a user with 2FA enabled, 5 wrong codes on `Disable2FA` return `ErrInvalid2FACode`. The 6th call, made with the **valid** current code, returns `ErrRateLimited`, and the user still has `TOTPEnabled = true` with an unchanged secret. Before the fix the 6th call succeeds and disables 2FA.
  - **Shared budget:** 5 wrong `Verify2FA` attempts, then `Disable2FA` with a valid code, gives `ErrRateLimited`.
  - **Positive control:** a fresh user with a valid code on the first try disables 2FA, and the counter entry is deleted afterwards.
- Handler test: the throttled call answers HTTP 429 with code `RATE_LIMITED`.
