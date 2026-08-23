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
Email = "admin@solidping.com"

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
    -d '{"email":"admin@solidping.com","password":"solidpass"}'
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

## Also worth a look

`admin@solidping.com` is a `.com` address on a domain we do not own
(`solidping.io` is ours). Password reset for the seeded account therefore mails
into someone else's domain. That is not exploitable on its own — reset requires
delivery to that address, and we do not control it either — but it means the
seeded account has no working recovery path, which argues further for generating
credentials rather than fixing them.

## Provenance

Found 2026-08-23 during the production bring-up of `solidping.io` on k8xp, from
outside the product repo. Infrastructure record and the rotation are noted in
`k8xp/k8s/solidping/overlays/prod/README.md`; the business-side record is in
`solidping-business/memory/decisions.md`. No product code was changed.
