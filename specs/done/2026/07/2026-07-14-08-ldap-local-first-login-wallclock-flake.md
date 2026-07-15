---
model: sonnet
effort: medium
---

# `TestLogin_LocalPasswordTakesPriorityOverLDAP` flakes under parallel-suite load because it asserts a wall-clock bound

## Problem

`TestLogin_LocalPasswordTakesPriorityOverLDAP`
([`server/internal/handlers/auth/ldap_service_test.go:555`](server/internal/handlers/auth/ldap_service_test.go:555))
configures an unreachable LDAP server (`ldap://127.0.0.1:1`), creates a user
**with a local password hash**, then asserts that `svc.Login(...)` completes in
**under 2 seconds**:

```go
start := time.Now()
resp, err := svc.Login(ctx, org.Slug, "localuser@example.com", "local-password", Context{})
require.Less(t, time.Since(start), 2*time.Second,
    "local-password login must not be delayed by an unreachable LDAP server")
require.NoError(t, err)
require.NotEmpty(t, resp.AccessToken)
```

It passes reliably in isolation (~0.1s) but failed once under `make test` (the
full parallel backend suite) with a real duration of **8.85s**
(`"8.846784625s" is not less than "2s"`). Per repo convention
(`feedback_flaky_tests_are_bugs`), this must be root-caused, not re-run past.

### Root cause: the timing bound tests the wrong thing and is load-sensitive

Investigating the fallback ordering shows the production code is **already
correct** — the local-password path never touches LDAP:

- `Service.Login` ([`service.go:462`](server/internal/handlers/auth/service.go:462))
  switches on the user's credentials:
  ```go
  switch {
  case user != nil && user.PasswordHash != nil && *user.PasswordHash != "":
      if verifyErr := s.verifyLocalPassword(ctx, user, password); verifyErr != nil { ... }
  default:
      ldapUser, ldapErr := s.authenticateViaLDAP(ctx, orgSlug, email, password)
      ...
  }
  ```
  A user with a local password hash takes the **first** branch and
  `authenticateViaLDAP` is never called — there is no LDAP dial, no
  `ldapDialTimeout` (10s, [`ldap_service.go:39`](server/internal/handlers/auth/ldap_service.go:39))
  in play at all. The short-circuit the spec worries about already exists.

Therefore the 8.85s is **not** an LDAP dial. Inside the timed window, `Login`
does: a DB `GetUserByEmail` round-trip (testcontainer Postgres), a
**deliberately-expensive password verification** (`verifyLocalPassword` →
argon2/bcrypt, which is CPU- and memory-bound by design), org resolution, and
JWT signing in `completeLogin`. Under the full parallel suite — many
testcontainers plus CPU/memory contention — the argon2 verify alone can balloon
from tens of milliseconds to seconds. The 2s wall-clock bound is a proxy for
"LDAP was not dialed," but it actually measures password-hashing cost under
load, which is exactly what spikes when the whole suite runs.

Two further facts make the timing assertion redundant as well as fragile:

1. **A mistaken LDAP dial here would fail *instantly*, not hang.** Nothing
   listens on loopback port 1, so `connect()` returns `ECONNREFUSED`
   immediately — timing would not reliably catch an erroneous LDAP attempt
   anyway.
2. **The functional assertions already prove local-first ordering.** If LDAP
   had been the auth path, the login would return an *error* (connection
   refused against the bogus server), so `require.NoError(err)` +
   `require.NotEmpty(resp.AccessToken)` already prove LDAP was never attempted.

The sibling test `TestLogin_SuperAdminNeverLockedOut_LDAPEnabledAndUnreachable`
([`ldap_service_test.go:594`](server/internal/handlers/auth/ldap_service_test.go:594))
uses the same unreachable-LDAP setup and asserts **only** `NoError` +
non-empty token, with no timing bound — and is stable. That is the established
pattern in this file.

## Proposal

This is a **test-only** fix — no production change is needed; the local-first
short-circuit is already correct.

**Preferred:** drop the wall-clock proxy and rely on the functional assertions
that already prove the invariant, matching the stable sibling test:

- Remove `start := time.Now()` and the `require.Less(time.Since(start),
  2*time.Second, ...)` line.
- Keep `require.NoError(err)` and `require.NotEmpty(resp.AccessToken)`.
- Update the test's doc comment: the proof that LDAP is never attempted for a
  local-password user is that login **succeeds** against an unreachable LDAP
  server (a mistaken LDAP path would return connection-refused), not that it is
  "fast."

**Alternative (stronger, if we want an explicit "LDAP never dialed"
guarantee):** replace the timing proxy with a direct spy rather than a
wall-clock heuristic — e.g. point the LDAP config at a fake listener that
records connection attempts, or inject a dial counter into `LDAPService`, and
assert **zero** dials. This asserts the invariant directly and is immune to
machine speed. Only worth it if we decide the ordering guarantee deserves a
dedicated, timing-independent check; otherwise the preferred option is
sufficient and consistent with the sibling test.

Do **not** simply raise the 2s threshold: 8.85s already blew past a plausible
raised bound, and any fixed wall-clock number remains hostage to
hashing/testcontainer contention.

### Verification

After the change, run the affected package and the full suite under load a few
times to confirm no flake:

```bash
go test ./server/internal/handlers/auth/ -run TestLogin_LocalPasswordTakesPriorityOverLDAP -count=5
make test   # or: go test ./...   (repeat a few times)
```

## Implementation Plan

Test-only change in `server/internal/handlers/auth/ldap_service_test.go`, in
`TestLogin_LocalPasswordTakesPriorityOverLDAP` (~line 555-587):

1. Remove `start := time.Now()` and the subsequent
   `require.Less(t, time.Since(start), 2*time.Second, ...)` assertion.
2. Keep `require.NoError(t, err)` and `require.NotEmpty(t, resp.AccessToken)`
   unchanged — these alone prove LDAP was never dialed, since a mistaken LDAP
   attempt against the unreachable `ldap://127.0.0.1:1` server would fail
   instantly with connection-refused (an error), not succeed slowly.
3. Update the test's doc comment to state the real proof: "login succeeds
   against an unreachable LDAP server," mirroring the doc-comment style of the
   stable sibling test `TestLogin_SuperAdminNeverLockedOut_LDAPEnabledAndUnreachable`
   (same file, ~line 589-625), which uses the identical unreachable-LDAP setup
   with no timing assertion.
4. Run `make fmt`, then `make build-backend lint-back test`.
5. Run the target test repeatedly (`-count=5`) and the full auth package a
   couple of times (`-count=2`) to confirm stability under repeated execution.
6. No production code changes — the local-first short-circuit in
   `Service.Login` is already correct.
