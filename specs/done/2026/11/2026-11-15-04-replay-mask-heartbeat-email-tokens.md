---
model: opus
effort: medium
---

# The heartbeat URL and the email check address are recorded by the unmasked session replay

## Problem

Found by the security scan of 2026-11-15 (finding AN4). Severity: Low. Nobody has to act. It happens when PostHog is enabled and a member opens the checks list or the page of a heartbeat or email check. The recipient is whoever can read the PostHog project.

Session replay records page text unmasked on purpose. Only credentials go through `SecretText` (class `ph-no-capture`), as the policy in `web/dash0/src/lib/analytics.ts:22-31` says. Two check credentials are rendered as plain text on every visit, not shown once:

- **Heartbeat.** The URL with `?token=…`, and the matching curl command (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:365-366`, rendered in `<span>`s at `:432` and `:446`).
- **Email check.** The full inbound address `<token>@<domain>`:
  - on the detail page (`checks.$checkUid.index.tsx:643-655`);
  - in the list row's `title` attribute (`web/dash0/src/routes/orgs/$org/checks.index.tsx:509-513`), which rrweb records with the DOM.

Either token is enough to report the check up or down: the heartbeat token is its bearer credential, and the email token alone routes mail to the check (`server/internal/checkers/checkemail/config/secret_fields.go`). The same tokens are already redacted from config-as-code exports. The `secret-local/require-secret-text` lint rule misses these because the variables are named `heartbeatUrl` / `curlCommand` / `address` and the elements are `<span>`s.

AJ5 and AK4 (`specs/done/2026/11/2026-11-11-05-…`, `2026-11-12-04-…`) covered shown-once secrets and inputs. These are persistent ones.

## Proposal

1. Render the heartbeat URL, the curl command and the email address through `SecretText` (`web/dash0/src/components/ui/secret-text.tsx`), or wrap their container in `ph-no-capture`. Keep the copy buttons working.
2. In the checks list, drop the full address from the `title` attribute: show the truncated form only, or wrap the cell in `SecretText`.
3. Extend `secret-local/require-secret-text` (or add a unit test) so that a value built from `config.token` cannot be rendered outside `SecretText`.

## Tests

- dash0 unit test (next to `secret-text.test.tsx`): the heartbeat endpoint and the email endpoint components render their URL, curl command and address inside a `ph-no-capture` element. This fails before the fix. Positive control: the check name next to them is not wrapped.
- The checks list row of an email check carries no `title` containing the full token.
- Existing Playwright check-detail specs still find and copy the heartbeat URL and the email address.
