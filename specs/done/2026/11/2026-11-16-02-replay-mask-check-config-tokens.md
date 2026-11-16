---
model: opus
effort: medium
---

# The check detail page's Configuration card records the heartbeat/email token and the SMTP `delivery_to` in the session replay

## Problem

Found by the security scan of 2026-11-16 (finding AO1). Severity: Low. This is a gap left by the AN4 fix (`specs/done/2026/11/2026-11-15-04-replay-mask-heartbeat-email-tokens.md`, #880). Nobody has to act: PostHog has to be enabled, and a member opens a heartbeat, email or SMTP send-probe check. The recipient is whoever can read the PostHog project.

- #880 moved the endpoint rendering into `web/dash0/src/components/checks/check-endpoints.tsx` and wrapped the heartbeat URL, the curl command and the email address in `SecretText`.
- The same page still renders `<CheckConfigView check={check} />` for any check with a non-empty config (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:1640-1646`).
- `CheckConfigView` prints every scalar config key as plain text in a `<dd>` (`web/dash0/src/components/checks/check-config-view.tsx:99-118`, `ScalarValue` at `:33-48`). Nested scalar maps get the same treatment (`:130-138`). Nothing is wrapped in `ph-no-capture`, and no key is filtered out.
- The API sends these values to dash0 in clear on purpose, so the card shows them:
  - `token` on heartbeat checks (`server/internal/checkers/checkheartbeat/config/secret_fields.go:19-21`, public at rest, `ExportRedactedFields` = `token`);
  - `token` on email checks (the same pattern in `checkemail/config/secret_fields.go`);
  - `delivery_to` on SMTP send-mode checks. It is the email check's tokenized inbound address (`server/internal/checkers/checksmtp/config/secret_fields.go:9-25`).
- So the replay records the very token #880 hid one card further down. Either token is enough to report the check up or down.
- The `secret-local/require-secret-text` lint rule cannot catch this: the value arrives under the generic prop name `value`.

## Proposal

1. In `CheckConfigView`, render a config value through `SecretText` (or inside a `ph-no-capture` container) when its key is a credential. The server already names these keys: the checker's `ExportRedactedFields()` (`token`, `delivery_to`) and its declared `SecretFields()` (in case one is ever returned unredacted). Pick one of two ways:
   - **Recommended:** the check response carries the names, e.g. a `redactedConfigKeys` list, so dash0 never needs a hard-coded copy.
   - **Simpler:** a dash0 list keyed by check type, next to the existing hidden export keys, with a unit test that fails when a checker adds a key it does not list.
2. Keep the rest of the card readable. Only the matching rows are masked.
3. Document the rule next to `SecretText` in the design reference: a generic config renderer must mask credential keys.

## Tests

- `web/dash0/src/components/checks/check-config-view.test.tsx`:
  - for a heartbeat check, the `token` value renders inside an element with the `ph-no-capture` class;
  - the same for an email check `token`, and for an SMTP check `delivery_to`.
  - These fail before the fix. Positive control: a non-secret key on the same check (`period`, `host`) is not wrapped.
- Extend the Playwright `check-endpoints-no-capture.spec.ts` (#880) so that it also asserts that no text node outside `ph-no-capture` on the check detail page contains the token.
