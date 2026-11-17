---
model: opus
effort: low
---

# The check import dialog records pasted monitoring exports, credentials included, in the session replay

## Problem

Found by the security scan of 2026-11-17 (finding AP3). Severity: Low. No attacker action is needed: any member who imports checks (the convert route needs org admin, which on SaaS is anyone who creates an org) leaks the credentials of the document they paste to the analytics project.

- dash0 records sessions unmasked on purpose, typed values included (`web/dash0/src/lib/analytics.ts:345-353`). Only elements with `ph-no-capture` stay out of the replay (`web/dash0/src/lib/no-capture.ts:1-21`).
- The import dialog's payload `Textarea` (`id="import-payload"`, `web/dash0/src/routes/orgs/$org/checks.index.tsx:2218-2231`) carries no `data-secret-input`, so `Textarea` does not add `NO_CAPTURE_CLASS` (`web/dash0/src/components/ui/textarea.tsx:13`). A file loaded with the upload button lands in the same textarea.
- The documents it accepts routinely hold credentials: an Uptime Kuma backup has monitor basic-auth passwords, database connection strings and notification settings (bot tokens, SMTP passwords, webhook URLs with tokens); a Gatus config has alerting provider tokens and request headers; a SolidPing apply document written by hand can hold check passwords and secret headers.
- The Pingdom token field next to it is already masked (`PasswordInput`), and so is the rest of the shown-once and credential input set (GHSA-c9qq-4m99-c7f6, findings AK4 and AO1). This textarea was missed.

## Proposal

1. Add `data-secret-input` to the `import-payload` `Textarea` in `checks.index.tsx`, so the value never reaches the replay.
2. Check the other free-text inputs that take a third-party document or credential for the same gap (the apply/YAML editor if it has one, `checks.describe.tsx`), and mask any that can carry one. Leave inputs the author composes themselves (script editors) alone: that was ruled out before.
3. If the eslint rule `require-secret-text` can flag a `Textarea` whose `id`/`name` matches `payload|import|config`, extend it. Otherwise skip this step.

## Tests

- `web/dash0/e2e/check-endpoints-no-capture.spec.ts` (or a sibling spec): open the import dialog, choose a file source, and assert the `import-payload` textarea has the `ph-no-capture` class. This fails before the fix. Positive control: the dialog's plain `Label` stays capturable (no `ph-no-capture`).
- Component test on `Textarea`: with `data-secret-input` it gets `ph-no-capture`, without it does not.
