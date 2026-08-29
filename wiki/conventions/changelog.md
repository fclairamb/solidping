# Changelog Conventions

The root [`CHANGELOG.md`](../../CHANGELOG.md) is maintained by release-please and is
also the source for the public docs page at `/docs/changelog`
([web/docs/scripts/gen-changelog.ts](../../web/docs/scripts/gen-changelog.ts) generates
`web/docs/docs/changelog.md` from it at every docs build — see
[web/docs/src/lib/changelog.ts](../../web/docs/src/lib/changelog.ts) for the transform
itself). There is no separately maintained copy: whatever lands in the root file is what
users will read.

## Write entries as user-facing prose

A changelog entry is release-please's `fix:`/`feat:` commit message, expanded during the
`chore(release)` PR pass into full prose before the release is tagged. Write that
expansion for the person running SolidPing, not for a future reader of the diff:

- Describe **what changed for the user** — what they can now do, what stopped breaking,
  what got faster — not the internal mechanism, unless the mechanism is itself the
  user-visible behavior (e.g. "server-side EWMA accounting" is fine when explaining why
  numbers are now accurate).
- An internal-only refactor, dependency bump, or CI change either stays out of the
  changelog entirely, or is scoped under `deps` — the docs-page transform drops every
  `deps`-scoped bullet, so that's the deliberate way to keep something changelog-accurate
  (for auditing) without putting it in front of users.
- It's fine for an entry to be long and detailed — the transform does not truncate or
  summarize. Being specific about the failure mode a fix addresses is more useful to a
  reader deciding whether an update matters to them than a one-line summary.

## Scopes come from the transform's lookup table

The `**scope:**` prefix on a bullet (e.g. `**dash0:**`, `**sftp:**`) is rewritten to a
product-facing name via the `SCOPE_LABELS` table in
[web/docs/src/lib/changelog.ts](../../web/docs/src/lib/changelog.ts) (`dash0` → Dashboard,
`sftp` → SFTP checks, `auth` → Authentication, and so on). A scope not in that table is
**not** an error — it passes through unchanged, verbatim, so the docs build never breaks
on a new one. But it does mean the rendered page shows the raw slug instead of a proper
name, so:

- If a scope will show up regularly (a new feature area, a renamed area), add its display
  name to `SCOPE_LABELS` in the same PR that introduces the scope, or as a quick follow-up.
- This is a lookup table edit only — no change to release-please configuration or to how
  commits are scoped.

## Check the rendered page before a release ships

The `chore(release)` PR pass — the point where the changelog entries are expanded from
commit messages into prose — is the right moment to also check how the page will actually
render. Run `bun run start` in `web/docs` (it regenerates the changelog page on every
start, same as the production build) and open `/docs/changelog` locally. Look for:

- A scope slug that rendered raw instead of as a proper name (add it to `SCOPE_LABELS`).
- A bullet that reads like an internal note rather than user-facing prose (rewrite it, or
  move it under a `deps`-scoped entry if it's not user-facing at all).

The transform itself is intentionally tolerant — an unrecognized heading or bullet shape
passes through verbatim rather than failing the docs build — so a rough entry will still
render, just not as cleanly as a conforming one.
