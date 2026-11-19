---
model: opus
effort: medium
pr: batch
automerge: false
---

# First boot seeds the superadmin with a public password

GitHub Issue: https://github.com/fclairamb/solidping/issues/943

## Source

A user on the feddit.org project post (comment https://feddit.org/comment/16144435,
2026-11-18) suggested generating a random password at first start and printing it to the
console, since the operator is forced to change it anyway. The report is right: the forced
rotation closes the exposure only once someone logs in, and until then the credential pair is
public.

## Problem

- `server/internal/jobs/jobtypes/job_startup.go:341` seeds the first superadmin with
  `defaults.Password` (`solidpass`, `server/internal/defaults/defaults.go`), and
  `admin@solidping.io` is the default email (`SP_ADMIN_EMAIL` overrides it). Both are
  published in the repo and the docs.
- `job_startup.go:361` sets `MustChangePassword = true` (spec 2026-08-23-04), so the first
  login can only rotate the password. But that first login is open to whoever reaches the
  instance first: on a fresh install exposed to the internet (Fly, Railway, Coolify,
  CasaOS…), a scanner that logs in before the operator takes over the superadmin by choosing
  the new password.
- `server/internal/app/ready.go:51` (`logFirstLogin`) already prints the email and password at
  boot while `must_change_password` is set. Its comment says the printed password is "a
  constant (defaults.Password, never configurable), and it is the only password this line can
  print": the invariant this spec has to change.
- The password is hashed with bcrypt (`job_startup.go:347`) because it is public and an
  argon2id derivation at every first boot doubled the memory peak (spec 2026-10-12-04). A
  random password stays hashed once per install, so bcrypt remains fine: it is rotated to
  argon2id at the forced change.
- `solidpass` is relied on by dev and test flows: `web/dash0/e2e/forced-password-change.spec.ts`,
  `web/dash0/e2e/checks.spec.ts`, `web/dash0/e2e/README.md`, `docker-compose/scripts/README.md`,
  `server/test/integration/startup_forced_rotation_test.go`,
  `server/internal/app/ready_log_test.go`, the CLI defaults (`server/pkg/cli/context.go`,
  `server/pkg/cli/config/config.go`), and many install docs (`README.md`, `web/docs/docs/intro.md`,
  `installation/*.md`, `configuration/authentication.md`, `deploy/helm/.../NOTES.txt`,
  `deploy/casaos/.../docker-compose.yml`).

## Why a spec

- Correctness hinges on authorization scope (the bootstrap superadmin credential).
- A product decision the reporter didn't settle: which run modes keep `solidpass`, and how the
  operator retrieves a random password on platforms with no console access.
- Over ~4 source files: the seed, the ready line, config/env, tests, the CLI and a dozen docs.

## Proposal

1. **Config**: `auth.admin_password` / `SP_ADMIN_PASSWORD` (next to `SP_ADMIN_EMAIL` in
   `server/internal/config/config.go`, `applyAuthEnv`). When set, the seed uses it (still
   `MustChangePassword = true`): automation and PaaS templates keep a known first password.
2. **Seed** (`ensureDefaultOrganization`): password source, in order: `SP_ADMIN_PASSWORD`;
   `defaults.Password` in the `test` and `demo` run modes (see open question 1);
   otherwise a random password (`crypto/rand`, 20 characters from an unambiguous alphabet,
   about 115 bits). Keep bcrypt.
3. **Retrieval**: the random password is never stored in clear in the database. Print it at
   seed time in a dedicated, prominent WARN line (email + password + "shown once, you will
   be asked to change it"), and make `logFirstLogin` print the password only when it can
   know it: `solidpass` in the modes that use it, nothing but the email and "see the
   first-boot log line or set SP_ADMIN_PASSWORD" otherwise (see open question 2). Rewrite the
   `logFirstLogin` comment, whose invariant no longer holds.
4. **Tests**: the seed in production mode yields a password that is not `solidpass`, differs
   between two installs, and logs in once (then forces the rotation); `SP_ADMIN_PASSWORD`
   wins; `test` mode still seeds `solidpass` (existing e2e keep working). Update
   `ready_log_test.go` and `startup_forced_rotation_test.go`.
5. **Docs**: every page that tells the user to log in with `solidpass` explains where to find
   the generated password (`docker logs <container> | grep "First login"`, `kubectl logs`,
   `fly logs`), and how to pin one with `SP_ADMIN_PASSWORD`. Helm NOTES and the CasaOS compose
   either set `SP_ADMIN_PASSWORD` from a generated value or point at the logs. Changelog
   entry flagged as a behaviour change for new installs (existing installs are untouched: the
   seed only runs on an empty database).

## Open questions

1. **Which run modes keep `solidpass`?** Recommended: `test` and `demo` keep it, the
   default (production, `SP_RUN_MODE` unset) gets a random password. Trade-off: a mode-dependent posture
   is what spec 2026-08-23-04 avoided ("one more thing that can be mis-detected"), but a
   mis-detection here only falls back to the old behaviour, and the e2e, local dev and demo
   flows keep working without change. Alternative: random everywhere, with the e2e and dev
   scripts setting `SP_ADMIN_PASSWORD=solidpass`.
2. **Reprint the random password at every boot until it is rotated?** Recommended: no, print
   it once at seed time. Re-printing would need it stored in clear (or reversibly) in the
   database, which defeats the point. Recovery for an operator who lost the line: while the
   seeded admin still has `must_change_password` set (never logged in), a boot with
   `SP_ADMIN_PASSWORD` set re-hashes that account's password to it (no other account, never
   after the rotation). The only existing reset is the emailed token flow
   (`solidping auth reset-password`), which needs SMTP. Trade-off: on PaaS with short log
   retention the line can be missed, hence the `SP_ADMIN_PASSWORD` route in every PaaS doc.

## Closing the issue

This spec closes #943. The implementing PR body **must** carry a `Closes #943` line so the
squash-merge closes it. When the spec is archived to `specs/done/`, verify the issue is
closed and, if the merge did not close it, close it by hand:

    gh issue close 943 --repo fclairamb/solidping --comment "Implemented by <PR or commit>; spec: <archived spec path>"
