---
model: sonnet
effort: low
---

# `docker run ghcr.io/fclairamb/solidping` fails to start most of the time, because Docker's default hostname starts with a digit

## Problem

The most basic way to run SolidPing — the one a new self-hoster tries first —
refuses to boot roughly three times in four:

```
level=ERROR msg="Invalid configuration" error="invalid worker slug: \"063429309bca\"
(derived from hostname \"063429309bca\") does not match ^[a-z][a-z0-9-]{2,20}$
— set SP_NODE_NAME to an explicit worker name"
```

Docker's default container hostname is the 12-character hex container ID. The
worker slug must match `WorkerSlugPattern`
(`server/internal/config/worker_identity.go`), which mirrors the
`workers.slug` CHECK constraint. Both demanded a leading letter,
`^[a-z][a-z0-9-]{2,20}$`. A hex ID begins with `0-9` for 10 of its 16
possible first characters, so the derived slug fails validation and the
process exits.

Measured, not reasoned about: **9 of 12** freshly created containers drew a
hostname starting with a digit.

The failure is worse than its rate suggests, because it is not deterministic.
It presents as a flaky image — the same command someone saw working in a
README or a blog post fails for them, then works if they retry enough times.
The first `docker run` written for the README passed on the first try and was
committed as verified; it was a coin flip.

### This was a known gap, deliberately left open

`sanitizeHostnameSlug` said so itself: the residue left after substituting
illegal characters — "starts with a digit, shorter than 3 chars, or empty" —
was left for `Validate()` to reject, and a test pinned it (`"hostname
sanitizes to a leading digit"`, hostname `10.0.0.1`).

That was a reasonable call when the motivating case was the Kubernetes
`hostNetwork` dotted node name (`eu2.example.com` → `eu2-example-com`). A
leading digit looked like an operator's misconfiguration. It is not: it is
what Docker hands every container by default, and containers are how
SolidPing is distributed — the GitHub releases carry no binaries, so
`ghcr.io` is the only packaged way to run it.

## Decision: relax the pattern, do not rewrite the hostname

The first draft of this spec proposed prefixing the derived slug (`w-063…`)
so it would start with a letter. That was the wrong shape of fix. It makes the
registered slug something the operator never chose and cannot predict from
`hostname`, it adds a second sanitization rule to reason about, and it exists
only to satisfy a requirement nothing actually has.

Nothing needs the leading letter. The slug is an opaque upsert key — it is
never a DNS label, a Go identifier, a Kubernetes name, or a token in any other
grammar that forbids a leading digit. The letter was a habit borrowed from
slug rules elsewhere in the codebase (check slugs, status-page slugs, region
slugs) where it also mostly is a habit, but those are out of scope here.

So the fix is to admit the digit: `^[a-z0-9][a-z0-9-]{2,20}$`, on both sides
of the mirror.

### Why this cannot move an existing deployment's `workers` row

Every slug that satisfied the old pattern satisfies the new one, so no slug
that registered yesterday registers differently today, and the fixed-point
property of `sanitizeHostnameSlug` (an already-valid hostname is returned
byte-identical) is untouched. The only deployments whose behaviour changes are
the ones that could not boot at all.

## Proposal

1. **Postgres:** a new consolidated migration `018_v0_24_0` (v0.23.1 is the
   last tag, the open release-please PR is v0.24.0, and `017` is released) with
   one `worker-slug-leading-digit` SECTION that drops `workers_slug_check` and
   re-adds it as `check (slug ~ '^[a-z0-9][a-z0-9-]{2,20}$')`. The down half
   restores the old pattern and is allowed to fail if a digit-leading worker
   has registered — parity only, never run in production.

2. **SQLite:** a statement-free `018_v0_24_0` mirror. Its `workers.slug`
   CHECK has been length-only since `001`, so a digit-leading slug was always
   storable there; the leading-letter rule lived solely in the Go pattern. The
   file exists so both dialects keep the same `NNN` sequence.

3. **Go:** `WorkerSlugPattern` becomes `^[a-z0-9][a-z0-9-]{2,20}$` and its
   doc comment now points at `018` and says why the digit is allowed. The
   `sanitizeHostnameSlug` comment no longer lists "starts with a digit" as
   residue. No code path changes — the regexp is the whole fix.

4. **`SP_NODE_NAME`:** still used verbatim, still a hard error when invalid.
   A leading digit is now simply valid there too; a leading dash still is
   not.

5. **Tests:** the drift guard (`TestWorkerSlugPatternMatchesMigration`) pins
   the new literal. `063429309bca` is added to the hostname-fallback table as
   a fixed point (boots, `Sanitized` false). `10.0.0.1` moves from the
   invalid table to the sanitization table (`10-0-0-1`, boots). The invalid
   override case `1solidping` becomes `-solidping`, which the new pattern
   still rejects, so the override contract stays pinned. The fixed-point test
   for already-valid hostnames passes untouched — it is what proves no
   existing deployment moves.

6. **Docs:** the pattern quoted in `web/docs/docs/configuration/index.md`
   and `wiki/conventions/runners.md` is updated, and the "unreleased
   migration right now" pointer in `wiki/conventions/database.md` now names
   `018_v0_24_0`.

## Out of scope

The README works around this today by passing `--hostname solidping`
(solidping#332). That can stay: pinning a worker name is good practice, and
it keeps the documented command working on older images. But it is a
workaround for a default that no longer needs one.

The other slug grammars in the codebase that prepend a letter to a
digit-leading value (checks, status pages, discovery suggestions) are left
alone. They are user-visible URL slugs with their own conventions, not worker
identities, and none of them is on the `docker run` path.
