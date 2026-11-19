---
model: haiku
effort: low
---

# The revealed PostHog personal API key on Server → Analytics is recorded in session replay

## Problem

Found by the security scan of 2026-11-19 (finding AR4). Severity: low. Who can trigger it: no attacker. A super admin who types or reveals the key exposes it to everyone who can read the instance's PostHog session replays.

- dash0 records sessions unmasked on purpose (`web/dash0/src/lib/analytics.ts:345-353`). A field stays out of the replay only if it has `ph-no-capture`, which `Input` adds for `data-secret-input` (`web/dash0/src/components/ui/input.tsx:15`, `web/dash0/src/lib/no-capture.ts`).
- The personal API key field (`web/dash0/src/routes/orgs/$org/server.analytics.tsx:258-266`) toggles between `password` and `text` and carries neither marker. Once the eye button reveals it, rrweb records the typed value.
- Every sibling reveal-style secret got the class in #851 (finding AK4): `server.web.tsx:147`, `server.slack.tsx:221`, `server.mail.tsx`, `server.email-inbox.tsx`, `components/ui/password-input.tsx:49`. This page was missed.
- The `require-secret-input` lint rule could not catch it: its `SECRETISH` pattern (`web/dash0/eslint-rules/require-secret-input.js:15`) does not match `posthogPersonalApiKey`.
- A personal API key reads and writes the whole PostHog organization it belongs to.
- The project API key on the same page (`posthogProjectApiKey`) is the public ingest token, served to every browser: not affected.

## Proposal

1. Add `className={NO_CAPTURE_CLASS}` (or `data-secret-input`) to the personal API key `Input` in `server.analytics.tsx:258`, as `server.web.tsx:147` does.
2. Widen `SECRETISH` in `eslint-rules/require-secret-input.js:15` with `api[-_]?key` and `personal`, so the class of miss is caught. Fix or mark any new hit it raises: hits that are not secrets (the public project key) get an explicit allow comment.

## Tests

- The eslint rule's own test (`web/dash0/eslint-rules/*.test.*`, or add one): an `Input` whose id is `posthogPersonalApiKey` with a `type` toggle and no marker is reported. This fails before the rule change.
- Positive control: the same `Input` with `NO_CAPTURE_CLASS` is not reported.
- `bun run lint` on `server.analytics.tsx` is clean after the fix.
