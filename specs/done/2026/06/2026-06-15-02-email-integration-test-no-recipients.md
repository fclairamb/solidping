# Email integration "Send test" always reports "no recipients configured"

## Context

On the integration edit page `…/integrations/<uid>`, the **Send a test notification**
section's **Send test** button fails for an **email** integration that has recipients
configured. The result badge shows `— · 0 ms — no recipients configured`, even though the
**Recipients (one per line)** field is populated (e.g. `florent.clairambault+ops@gmail.com`).

Reported on
`https://solidping.k8xp.com/dash0/orgs/default/integrations/a6552af1-37aa-4288-a13c-2d8e0beeba8c`
(org `default`). The same mismatch means **real** email notifications never resolve their
recipients either — this is not specific to the test path.

### Root cause — confirmed by code read (frontend write vs. backend read)

The recipients are written and read under **two different keys**.

- **Frontend writes `to`.** The email panel in
  [`web/dash0/src/components/integrations/integration-form.tsx:256-282`](../../web/dash0/src/components/integrations/integration-form.tsx)
  reads `settings.to` and, on change, calls `update("to", [...])` — splitting the textarea on
  newlines into a `string[]`. So a saved email integration stores
  `settings = { "to": ["florent.clairambault+ops@gmail.com"] }`.

- **Backend reads `recipients`.** `EmailSender.Send`
  ([`server/internal/notifications/email.go:72-87`](../../server/internal/notifications/email.go))
  does:

  ```go
  recipientList, ok := payload.Integration.Settings["recipients"].([]any)
  if !ok || len(recipientList) == 0 {
      return ErrNoRecipientsConfigured // "no recipients configured"
  }
  ```

  The `"recipients"` key is never present (the frontend has only ever written `"to"`), so
  `ok == false` and `ErrNoRecipientsConfigured` is returned immediately. That error string is
  what the test badge renders.

The test endpoint itself is fine: `POST /orgs/:org/integrations/:uid/test` →
`TestIntegration` ([`server/internal/handlers/integrations/service.go`](../../server/internal/handlers/integrations/service.go))
loads the integration, merges decrypted settings, and calls the same `EmailSender.Send`. So
the failure is purely the key mismatch — the "save first" note in the UI is correct and not the
issue.

### Why the canonical key is `to` (not `recipients`)

- **Existing data already uses `to`.** Every email integration created through the UI is stored
  with `settings.to`; nothing has ever written `settings.recipients`. Aligning the reader on `to`
  fixes existing records with **no data migration**. Aligning on `recipients` instead would orphan
  all existing integrations and require a backfill — strictly worse.
- **The rest of the email subsystem uses `to`.** `email.Message{Recipients: email.Recipients{To: …}}`
  ([`server/internal/email/email.go:8-16`](../../server/internal/email/email.go)) and the generic
  email job config `To []string \`json:"to"\``
  ([`server/internal/jobs/jobtypes/job_email.go:29`](../../server/internal/jobs/jobtypes/job_email.go))
  both use `to`. `email.go:73` is the **only** place in the backend that reads `recipients`.
- "Recipients" is human-facing copy only (the i18n label `form.recipients` and the error string);
  it does not need to be the storage key.

### Why this went unnoticed

There is **no unit test** for `EmailSender.Send` — `server/internal/notifications/` has `email.go`
but no `email_test.go`. A single test that constructs an integration with `settings.to` and asserts
recipient resolution would have caught the mismatch.

## Goals

- "Send test" on an email integration with recipients resolves them and no longer returns
  "no recipients configured" (it should proceed to actual delivery, succeeding when SMTP is
  configured or failing with a *different*, delivery-related error otherwise).
- Real email notifications resolve their recipients from the saved `to` setting.
- Existing email integrations (already stored under `to`) work without any data migration.
- A regression test pins the recipient-resolution behavior.

## Implementation

### 1. Core fix — read `to`

In [`server/internal/notifications/email.go:72-87`](../../server/internal/notifications/email.go),
read recipients from `settings["to"]` instead of `settings["recipients"]`.

While here, make parsing defensive about the value type — JSONB load yields `[]any`, but
in-memory construction (tests, future callers) may yield `[]string`. Accept both, and keep the
existing element-level string filtering. Keep the two error sentinels (`ErrNoRecipientsConfigured`
when the key is absent/empty, `ErrNoValidRecipients` when no element is a usable string); the
wording "no recipients configured" still matches the UI label.

No frontend change is needed — `integration-form.tsx` already reads and writes `settings.to`.

### 2. Regression test — new `email_test.go`

Add `server/internal/notifications/email_test.go` covering `EmailSender.Send` (and/or
`buildEmailContent`/recipient extraction if a smaller seam is cleaner). Table-driven, per the
backend testing conventions (`t.Parallel()`, `testify/require`):

- recipients under `to` as `[]any{"a@x"}` → resolves, attempts send (no `ErrNoRecipientsConfigured`);
- recipients under `to` as `[]string{"a@x","b@y"}` → resolves both;
- `to` absent or empty → `ErrNoRecipientsConfigured`;
- `to` present but holds no usable string → `ErrNoValidRecipients`;
- (guard against regression) `recipients` key present but `to` absent → still
  `ErrNoRecipientsConfigured`, documenting that `to` is the canonical key.

Use a stub/fake `EmailSender` service in `jctx.Services` so the test asserts the `email.Message`
recipients without sending real mail.

### 3. End-to-end (optional but preferred)

Per the dash0 testing convention, prefer a Playwright e2e in `web/dash0/e2e/`: create/edit an email
integration with a recipient, save, click **Send test**, and assert the result badge does **not**
show "no recipients configured". Gate on a configured/fake SMTP sender in test mode so the assertion
is about recipient resolution, not real delivery.

## Verification

1. **Reported record:** on `…/integrations/a6552af1-37aa-4288-a13c-2d8e0beeba8c`, save, then
   **Send test** → badge no longer reads "no recipients configured".
2. **Unit:** `email_test.go` cases above pass; the `to`-absent / `recipients`-only cases still error.
3. `make test` and `make lint` pass.

## Files referenced

- `server/internal/notifications/email.go` — recipient extraction (the fix) + new `email_test.go`.
- `web/dash0/src/components/integrations/integration-form.tsx:256-282` — confirms frontend uses `to`; no change.
- `server/internal/handlers/integrations/service.go` — `TestIntegration` / `loadDecryptedSettings` (read context).
- `server/internal/email/email.go`, `server/internal/jobs/jobtypes/job_email.go:29` — precedent for the `to` key.

## Implementation Plan

1. **Fix** (`email.go`): read `settings["to"]`, accept `[]any` and `[]string`, keep both error sentinels.
2. **Test** (`email_test.go`): table-driven coverage of resolution, empty, invalid, and `recipients`-only cases via a fake email sender.
3. **(Optional) e2e** (`web/dash0/e2e/`): edit email integration → Send test → assert no "no recipients configured".
