---
model: sonnet
effort: medium
---

# A hostname with dots (or any non-slug character) makes the worker refuse to start instead of deriving a sanitized slug

## Problem

On a stock macOS/LAN machine the server fails at boot:

```
level=ERROR msg="Invalid configuration" error="invalid worker slug: \"host-002.lan\" (derived from hostname \"Host-002.lan\") does not match ^[a-z][a-z0-9-]{2,20}$ — set SP_NODE_NAME to an explicit worker name"
```

The hostname-derived identity path in
[`resolveWorkerIdentity`](server/internal/config/worker_identity.go#L72)
already transforms the hostname — it lowercases it and truncates it to
`WorkerHostnameMaxLen` (15) — but it does **not** replace characters that are
illegal in the slug pattern
([`WorkerSlugPattern`](server/internal/config/worker_identity.go#L20),
mirroring the `workers.slug` CHECK constraint). Dots are the killer case:
`.lan`, `.local`, `.home` suffixes are the default on macOS and most home/office
networks, so the out-of-the-box experience on such machines is a hard startup
failure for what is only a *default*, not operator intent.

The file's own design already treats derived-identity hazards as warnings, not
errors: hostname truncation can collapse two workers onto one row, and that
gets a WARN + "set SP_NODE_NAME" advice
([`WarnIfTruncated`](server/internal/config/worker_identity.go#L124)), not a
refusal to boot. Failing hard only on illegal *characters* is inconsistent
with that stance.

## Proposal

Slugify the hostname-derived identity instead of rejecting it; keep the
explicit `SP_NODE_NAME` override strictly validated verbatim (operator intent
must not be silently rewritten — the current behavior there is correct).

In `resolveWorkerIdentity`, after lowercasing and truncating:

1. Replace every character outside `[a-z0-9-]` with `-`
   (`Host-002.lan` → `host-002-lan`, matching what an operator would expect).
2. Collapse runs of `-` and trim leading/trailing `-` (avoids `host--002` and
   trailing dashes left by truncation).
3. If the result still doesn't match `WorkerSlugPattern` (starts with a digit,
   shorter than 3 chars, empty), keep today's actionable error — that's the
   rare pathological case the error message was written for.

Properties worth asserting in tests:

- **No existing deployment changes slug.** Any hostname that passes validation
  today is a fixed point of the sanitization (already lowercase, already only
  `[a-z0-9-]`), and hosts that fail today never registered a row — so the
  change is strictly additive.
- Sanitization happens on the derived path only; `SP_NODE_NAME` is untouched
  and still fails hard when invalid.
- `Name` should stay the human-readable (truncated, original-case) hostname;
  only `Slug` is sanitized.
- When sanitization actually changed something (slug ≠ lowercased truncated
  hostname), log a WARN in the spirit of `WarnIfTruncated` — same collision
  caveat (`host-002.lan` and `host-002-lan` now share a slug), same
  "set SP_NODE_NAME to pin an identity" advice. Extending `WorkerIdentity`
  with a `Sanitized bool` mirrors the existing `Truncated` field.

Open question (default: no): strip the domain part instead of substituting the
dots (`host-002.lan` → `host-002`)? It yields prettier slugs but raises the
cross-domain collision risk and diverges from the substitution users can
predict; plain substitution is the safer default.

No migration needed — validation and the DB CHECK constraint are unchanged;
only derivation becomes more permissive.
