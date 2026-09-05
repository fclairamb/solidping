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
worker slug must match `WorkerSlugPattern`, `^[a-z][a-z0-9-]{2,20}$`
(`server/internal/config/worker_identity.go:20`), which mirrors the
`workers.slug` CHECK constraint. A hex ID begins with `0-9` for 10 of its 16
possible first characters, so the derived slug fails validation
(`server/internal/config/config.go:2815`) and the process exits.

Measured, not reasoned about: **9 of 12** freshly created containers drew a
hostname starting with a digit.

The failure is worse than its rate suggests, because it is not deterministic.
It presents as a flaky image — the same command someone saw working in a
README or a blog post fails for them, then works if they retry enough times.
The first `docker run` written for the README passed on the first try and was
committed as verified; it was a coin flip.

### This is a known gap, deliberately left open

`sanitizeHostnameSlug` (`server/internal/config/worker_identity.go:140`, doc comment from line 118) is
explicit about it:

> The pathological residue that remains after substitution (starts with a
> digit, shorter than 3 chars, or empty after collapsing) is left for
> `Validate()` to reject with its existing actionable error — sanitizing is
> not a promise that the result is valid.

and there is a test pinning that behaviour — `"hostname sanitizes to a leading
digit"`, hostname `10.0.0.1`
(`server/internal/config/worker_identity_test.go:368`).

That was a reasonable call when the motivating case was the Kubernetes
`hostNetwork` dotted node name (`eu2.example.com` → `eu2-example-com`), which
that spec fixed. A leading digit looked like an operator's misconfiguration.
It is not: it is what Docker hands every container by default, and containers
are how SolidPing is distributed — the GitHub releases carry no binaries, so
`ghcr.io` is the only packaged way to run it.

### Why fixing it cannot move an existing deployment's `workers` row

The rest of this file is (correctly) very careful that sanitization must never
rewrite a slug that already validates, because upsert-by-slug means a rewrite
silently moves an existing worker's row. That constraint does not bind here:
**every deployment this change affects is one that has never started.** An
invalid slug fails at `Validate()` before any database work, so there is no
`workers` row to move, and no existing registration to preserve. The set of
slugs whose behaviour changes is exactly the set that currently cannot boot.

## Proposal

1. In `sanitizeHostnameSlug`, after the existing substitute/collapse/trim
   steps, handle the two residue cases that remain — **only** when the result
   still fails `workerSlugRegexp`, so the early-return fixed point for
   already-valid hostnames is untouched:
   - result begins with a digit or a dash → prefix it to make it start with a
     letter;
   - result is shorter than the 3-character minimum (including empty) → pad it
     to the minimum.

   A prefix such as `w-` keeps the original hostname legible in the slug
   (`063429309bca` → `w-063429309bca`), which is what an operator scanning the
   worker list needs. The length works out without extra truncation: the
   pattern admits 3–21 characters, the hostname is already cut to
   `WorkerHostnameMaxLen = 15`, and 15 + 2 = 17 ≤ 21.

2. Do **not** apply any of this to the `SP_NODE_NAME` override path. An
   explicit operator value stays verbatim and stays a hard error when invalid
   — that is the existing contract at `worker_identity.go:88` and the
   `Validate()` branch that names `SP_NODE_NAME`, and silently rewriting an
   operator's chosen identity would be a genuine surprise.

3. Report it. The identity already carries `Sanitized`, and
   `WarnIfSanitized` already logs the collapse-risk warning; make sure a slug
   that needed the new prefix/pad is flagged the same way, so two containers
   whose IDs sanitize alike are still diagnosable.

4. Tests: replace the `"hostname sanitizes to a leading digit"` invalid-case
   assertion with a boots-successfully case, and add a realistic Docker
   container ID (`063429309bca`). Keep an invalid case for the override path
   so the `SP_NODE_NAME` contract stays pinned. The fixed-point test at
   `worker_identity_test.go:275` — already-valid hostnames returned unchanged
   — must keep passing untouched; it is what proves no existing deployment
   moves.

## Out of scope

The README works around this today by passing `--hostname solidping`
(solidping#332). That should stay: it is good practice to pin a worker name,
and it keeps the documented command working on older images. But it is a
workaround for a default that should not need one.
