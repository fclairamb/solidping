---
model: opus
effort: high
---

# The RP-ID-mismatch passkey login test is nondeterministic, and it may be hiding a silent login failure

## Problem

`web/dash0/e2e/login.spec.ts:470` — "Login: passkey error handling › shows the
domain-mismatch message (not the generic error) on an RP-ID mismatch" — passes or
fails depending on timing, on identical code.

Evidence (2026-09-20, two full-suite runs on the same commit):

- Run 1: passed first try (5.7s).
- Run 2: failed twice (5.9s, 6.1s), passed on retry #2 → reported as `1 flaky`.

Failure, both attempts:

```
Error: expect(locator).toBeVisible() failed
Locator: getByTestId('login-error')
Expected: visible
Timeout: 5000ms
Error: element(s) not found
at web/dash0/e2e/login.spec.ts:514:25
```

The assertion block is `login.spec.ts:512-516`: `login-error` must become visible,
must contain "domain", and must NOT contain "unexpected error". The failure mode is
*no error element at all* — the page surfaced **no** login error within 5s, rather
than surfacing the wrong one. If the app can genuinely reach that state, a passkey
failure is silently swallowed on the login page, which is a real product bug and the
more important half of this spec.

The test works by routing `**/api/v1/auth/passkeys/login/begin` and rewriting the
returned `publicKey.rpId` to `example.com`, so the browser throws a `SecurityError`
inside `navigator.credentials.get()` and `@simplewebauthn/browser` maps it to
`ERROR_INVALID_RP_ID`. Note the comment at `login.spec.ts:481-486`: the same route
intercept also hits the **background conditional-UI ceremony**, whose failure is
supposed to stay silent. Two ceremonies racing over the same intercepted endpoint and
the same error-state slot is the obvious suspect — e.g. the silent conditional-UI
failure clearing or winning the shared error state, or the explicit click landing
while the background ceremony's abort is in flight.

### Scope note

This is **not** caused by the batch on `batch/2026-09-19-02` (super-admin user
directory + Discord DM parity). Nothing in that batch touches WebAuthn, passkeys or
RP-ID handling; its only `handlers/auth/` changes are a new `discord_link.go` and a
7-line `link:`-state early branch in `discord.go`'s OAuth callback, both guarded by a
state prefix that login states never carry. Treat the flake as pre-existing.

One nearby change worth checking as a trigger: `github.com/go-webauthn/webauthn` was
bumped to **v0.18.2** on `main` (commit `cadacf75c`). Check whether the RP-ID
mismatch error path changed shape in that release — e.g. an error that used to reach
the frontend as a structured code now arrives as something the page does not render
into `login-error`.

## Proposal

1. **Reproduce first.** It is timing-dependent, so run the single test in a loop
   (20+ iterations) against a side-car test server rather than once. Recipe:
   dedicated `solidping_e2e` Postgres DB on an alternate port, `SP_RUNMODE=test`,
   `CI=true`, `E2E_BASE_URL=http://localhost:<port>/d/` (the trailing `/d/` is
   mandatory). Build dash0 **before** the backend — `make build-backend` alone
   embeds a stale frontend. See `wiki/` and the root `CLAUDE.md`. Do not disturb the
   user's `:4000` devloop.

2. **Localize the race.** Decide between:
   - *test-side*: asserting before the ceremony has actually been driven, the route
     handler resolving late, or the click landing before the button's handler is
     wired;
   - *app-side*: the error state never rendering `login-error` on one of the two
     failure paths — the browser rejecting the RP ID locally before any request
     versus the server rejecting it — or the background conditional-UI ceremony
     (whose failure is deliberately silent) racing the explicit one and clearing or
     suppressing the shared error slot.

   Instrument rather than guess: capture console/network on a failing iteration.

3. **Fix the underlying cause.** No longer timeout, no retry, no skip, no
   `waitForTimeout`. If the app can reach a state where a passkey failure surfaces
   nothing to the user, fix that state — every explicit (user-initiated) passkey
   failure must render an error, and the silent conditional-UI path must not be able
   to clear or pre-empt an explicit failure's message.

4. **Keep the existing assertions intact** (`login-error` visible, contains
   "domain", does not contain "unexpected error"). Add:
   - whatever synchronization/state fix makes the test deterministic;
   - an assertion for the "no error at all" bug if it turns out to be real (an
     explicit passkey failure always renders `login-error`);
   - a **positive control** proving the test still fails if the domain-mismatch
     message regresses to the generic one — e.g. a unit test over the
     error-code → message mapping asserting `ERROR_INVALID_RP_ID` maps to the
     domain copy and not the fallback.

## Gate

- `cd web/dash0 && bun run lint` — no **new** errors (~45 pre-existing react-hooks
  errors are known debt; leave them).
- `cd web/dash0 && bun run test:unit` green.
- The looped single-file E2E run (20+ iterations) green on **every** iteration.
- Read the root `CLAUDE.md` and `web/dash0/CLAUDE.md` first.
