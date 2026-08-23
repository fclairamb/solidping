---
model: sonnet
effort: medium
---

# An empty production database seeds admin@solidping.com / solidpass, as superadmin

## Problem

Starting the server against a database with no organizations creates a default
admin account. The credentials are constants in
[`server/internal/defaults/defaults.go`](server/internal/defaults/defaults.go):

```go
// Email is the default admin email created during initial setup.
// This account is created automatically when the server starts with no organizations.
Email = "admin@solidping.io"

// WARNING: This is a development/demo password. Change it immediately in production.
Password = "solidpass"
```

The package doc says these "should NEVER be used in production". Nothing
enforces that. The seeding is unconditional — it does not consult
`ENVIRONMENT`, `SP_DEPLOYMENT_MODE`, or anything else — so a production install
gets it too, and the seeded account is **`superadmin`**.

This is not theoretical. It happened on `solidping.io` on 2026-08-23. First
boot against a fresh production database seeded the account, and once the
ingress went up it was reachable from the internet:

```
$ curl -X POST https://solidping.io/api/v1/auth/login \
    -d '{"email":"admin@solidping.io","password":"solidpass"}'
{"accessToken":"eyJ…"}          # role: superadmin
```

The password has since been rotated, so that specific exposure is closed.

## Why this matters beyond our own deployment

SolidPing is self-hosted and the repository is public. Both halves of the
credential are in it. Every operator who follows the ordinary path — point the
binary at an empty database, put it behind a reverse proxy — gets an
internet-reachable superadmin account whose password is published on GitHub.
They get no warning at boot, and nothing in the UI flags it afterwards.

The window is not "until they finish setup" either. Nothing ever expires the
account or forces a rotation, so an install that nobody logged into as admin
stays exposed indefinitely.

For a monitoring product this is a particularly bad blast radius: superadmin
sees every org's checks, endpoints, and alert routing — which is a map of the
customer's infrastructure, including internal hosts reached by the private
worker.

## Suggested direction

The convenience is real for `docker run` and local dev; the fix should keep
that and close production. Roughly in order of preference:

1. **Refuse to seed a known-weak default outside development.** When
   `ENVIRONMENT`/deployment mode is anything but dev, either skip seeding and
   log a clear "no organizations exist; create the first admin with
   `solidping admin create`" line, or seed with a **generated** password
   printed once to stdout. The operator reads it out of the logs, exactly like
   the pattern used by databases and CI runners.
2. **Force rotation at first login.** If the seeded account keeps a fixed
   password, mark it `must_change_password` so the first successful login can
   do nothing else until it rotates. This alone converts a standing exposure
   into a race measured in seconds.
3. **Fail the readiness probe, or bannerize, while default credentials are
   still valid.** A deployment that is one curl away from superadmin should not
   report itself healthy and silent.
4. **Never log in as, or ship, `solidpass` in any image or compose file**
   without at least (2).

Option 1 plus 2 is probably the honest combination: dev keeps a zero-friction
path, production cannot start in a state the docs describe as never-for-prod.

## What has already landed (and what it did not fix)

`b6b211f49` ("chore: default the seeded admin to admin@solidping.io", 2026-08-23)
moved the seeded address from `admin@solidping.com` to `admin@solidping.io`
across `internal/defaults` and every place that documents or hardcodes it.

That closes a real side-issue this spec originally raised — the seeded account
was on a domain we do not own, so its password-reset mail went to a third party
and it had no working recovery path.

**It does not address the exposure above.** The password is still `solidpass`,
still a constant in the public repository, still seeded unconditionally, still
`superadmin`. Moving the address changes who cannot receive the reset mail; it
does not change who can log in. Everything under "Suggested direction" stands.

## Provenance

Found 2026-08-23 during the production bring-up of `solidping.io` on k8xp, from
outside the product repo. Infrastructure record and the rotation are noted in
`k8xp/k8s/solidping/overlays/prod/README.md`; the business-side record is in
`solidping-business/memory/decisions.md`. No product code was changed.

## Resolved open questions

Answered by the maintainer on 2026-08-23. These decisions **supersede** the
"Suggested direction" list above wherever they conflict — build what is written
here, not what the list ranks.

### Q: What should an empty database seed outside development? (options 1–4 above)

**Decision: option 2 only, and build it as a reusable primitive.** Keep seeding
the fixed default credentials exactly as today, and add a
`must_change_password` flag on the user that is set on the seeded admin. The
first successful login must be able to do *nothing* except rotate the password
until the flag is cleared.

Directives for the implementer:

- **Do not** generate a random password, and **do not** print a password to
  stdout. `admin@solidping.io` / `solidpass` stays the seeded credential pair —
  the forced rotation, not secrecy, is what closes the exposure.
- Model `must_change_password` as a **general, user-level capability**, not
  something bolted onto the admin-seed path. It will be reused to force
  rotation on ordinary users in later scenarios (operator-initiated resets,
  invited users, compromised-credential response). So: a column on `users`, a
  field on the model, honored centrally in the auth flow — never a special case
  keyed on "is this the seeded admin".
- The block belongs in the **auth layer**, so every authenticated surface is
  covered at once (API, dash0, CLI, PAT creation) rather than only the dashboard
  login form. A login that succeeds against a flagged account must yield a
  session that can reach the password-change endpoint and nothing else, and the
  response must carry a machine-readable signal so clients can route to the
  rotation screen instead of showing a generic 403.
- dash0 needs the matching screen: on that signal, land the user on a
  "set a new password" route that cannot be navigated away from.

### Q: How should the code tell "development" from "production"?

**Decision: it should not — drop this rule entirely.**

- Do **not** add `SP_ENVIRONMENT`, and do **not** branch seeding on
  `SP_RUNMODE` or `SP_DEPLOYMENT_MODE`. Seeding stays unconditional and byte-for-byte
  identical in dev and in production.
- Consequently, `make dev` against a fresh database will also prompt for a
  password rotation on first login. That is accepted and intended: one code
  path, no mode-dependent security posture, nothing that can be mis-detected.

### Scope notes that follow from the two decisions

- **Leave the test-mode seed alone.** `test@test.com` / `test` is created by a
  separate path (`createTestUser` in `server/test/testdata/testdata.go`), not by
  `ensureDefaultOrganization`. It must **not** get `must_change_password` — the
  dash0 and status0 Playwright suites log in with those fixed credentials and
  would all fail. Do not "consistently" apply the flag there.
- **Update the documented dev credentials** wherever they appear (root
  `CLAUDE.md`, `server/CLAUDE.md`, `web/docs/`) to note that the first login on a
  fresh database now requires setting a new password.
- **The CLI defaults to these credentials** (`pkg/cli/context.go`,
  `pkg/cli/config/config.go`). Make sure a CLI login against a flagged account
  fails with an actionable message naming the rotation, not an opaque error.
- Options 1, 3 and 4 from "Suggested direction" (generated password, skipping
  the seed, failing the readiness probe, bannerizing) are **out of scope**.
