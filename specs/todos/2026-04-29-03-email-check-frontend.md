# Email Passive Checks — Frontend & Admin UX

## Goal

Expose the work from `2026-04-29-01-email-inbox-jmap.md` and `2026-04-29-02-email-passive-checks.md` in `web/dash0`:

- A new "Email" option in the check create form.
- The check detail page surfaces the unique email address with a copy button and explanatory text (mirrors how heartbeat checks display their URL).
- The check list shows the email address (or its shortened form) in the "Target" column for `email`-type checks.
- A super-admin "Email Inbox" page that configures the shared JMAP connection and shows live status.

No new backend work — every endpoint we need was added in specs 01 and 02 (`GET/PUT system/parameters/email_inbox`, `GET system/email-inbox/status`, `POST system/email-inbox/test`, `POST system/email-inbox/sync`, plus the existing checks API).

## 1. TypeScript types

`web/dash0/src/api/hooks.ts` (or wherever the check-type union lives — same file edited for spec 02-16-heartbeat-checks):

- Add `"email"` to the `Check.type` union.
- Add `"email"` to the `CreateCheckRequest.type` union.
- Type for the `email_inbox` system parameter:

```ts
export type EmailInboxConfig = {
  enabled: boolean
  sessionUrl: string
  username: string
  // password is write-only — never returned by the GET endpoint
  addressDomain: string
  mailboxName?: string
  processedMailboxName?: string
  pollIntervalSeconds?: number
  processedRetentionDays?: number
  failedRetentionDays?: number
  rewriteBaseUrl?: string
}

export type EmailInboxStatus = {
  enabled: boolean
  connected: boolean
  lastSyncedAt?: string
  lastError?: string
}
```

Exported helper to compute the address:

```ts
export function emailCheckAddress(token: string, domain: string, status?: 'down' | 'error' | 'up' | 'running'): string {
  const local = status && status !== 'up' ? `${token}+${status}` : token
  return `${local}@${domain}`
}
```

## 2. Public `addressDomain` access

The `email_inbox` system parameter is `secret=true`, so its value is normally hidden from non-super-admins. Two paths to expose `addressDomain`:

- **Backend (preferred)**: extend `GET /api/v1/system/parameters/email_inbox/public` to return `{ "addressDomain": "..." }` for any authenticated user, with the rest of the value omitted. Add this to the system handler.
- **Fallback**: if the public projection doesn't exist, the create / detail screens fetch the parameter via the super-admin endpoint and degrade gracefully (show "Configure email inbox first" placeholder when 403).

Pick the backend route — it's a small handler and avoids 403 noise.

New hook: `useEmailAddressDomain()` that wraps `GET /api/v1/system/parameters/email_inbox/public` and caches via React Query. Returns `{ domain: string | null, isLoading, error }`. `domain` is `null` when the inbox isn't configured — components show the "Configure email inbox first" empty state.

## 3. Check create form

`web/dash0/src/components/shared/check-form.tsx`:

- Add `"email"` to the `CheckType` union.
- Append to `checkTypes` array: `{ value: "email", label: "Email", description: "Receive status updates via incoming email" }`.
- `handleSubmit`: email case sends `{ type: "email", name, period, ... }` with empty `config` — the backend auto-generates the token.
- `renderConfigFields`: email case renders an info block:
  - If the inbox isn't configured: warning callout — "Email inbox not configured. Ask your administrator to set it up under Admin → Email Inbox."
  - Otherwise: explainer — "An email address will be generated for this check. Send any email to that address to report a successful run. Use plus-addressing (`token+down@…`) or `[DOWN]` in the subject to report failure."
- The `period` label changes to "Expected Interval" when type is `email` (same treatment as `heartbeat`), with helper text "Check is marked down if no email is received within this interval."

## 4. Check detail page

`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx` (the same file modified by `2026-02-16-heartbeat-checks.md`):

For `check.type === "email"`, add an "Email Endpoint" section to the Configuration card with:

- The full address `<token>@<addressDomain>` rendered as a `<code>` block + copy-to-clipboard button.
- A collapsible "Reporting failure" panel listing the three options (plus-address, header, subject tag).
- A "Send test email" link (`mailto:<token>@<addressDomain>?subject=Test`) so users can verify the integration with their own client.

If `useEmailAddressDomain()` returns `null`, render a placeholder:

> Email inbox not configured. The check is created but cannot receive pings until an administrator configures the shared inbox.

## 5. Check list

`web/dash0/src/routes/orgs/$org/checks.index.tsx`:

- For `check.type === "email"`, the "Target" column shows `<token-prefix>@<addressDomain>` truncated (e.g. `4f9c2a1e…@ingest.solidping.example`) with a tooltip showing the full address. Falls back to `check.slug || "email"` when the domain is unknown.

## 6. Status page rendering

Email checks already participate via the standard result store, so status pages need no changes — the existing UP/DOWN icons cover them. Confirm during QA that an email check shows the right colour after a `+down` ingest.

## 7. Admin — Email Inbox page

New route: `web/dash0/src/routes/admin/email-inbox.tsx` (super-admin only — gated by the same auth guard used by `/admin/server` from `2026-02-18-server-admin-page.md`).

Layout — single page, three sections:

### a. Configuration

Form bound to `email_inbox` system parameter. Fields match the JSON shape from spec 01:

- `enabled` (toggle)
- `sessionUrl`, `username`, `password` (text / password)
- `addressDomain` (text, required when `enabled`)
- `mailboxName` (default `Inbox`)
- `processedMailboxName` (default `Processed`)
- `pollIntervalSeconds` (number, default 60)
- `processedRetentionDays` (number, default 30)
- `failedRetentionDays` (number, default 7)
- `rewriteBaseUrl` (text, optional, advanced)

Submitting calls `PUT /api/v1/system/parameters/email_inbox` with `secret: true`. Password field shows `••••••••` placeholder on edit; submitting an empty password keeps the existing one (server-side: leave `password` untouched if the body field is empty).

### b. Status

Shows live data from `GET /api/v1/system/email-inbox/status`, polled every 5s while the page is open:

- Connected / Disconnected pill.
- `lastSyncedAt` relative time.
- `lastError` (red panel) when set.

### c. Actions

- "Test Connection" button → `POST /api/v1/system/email-inbox/test` → toast with success / error.
- "Sync Now" button → `POST /api/v1/system/email-inbox/sync` → toast.
- Hint text linking to JMAP-capable providers (Fastmail, Stalwart, custom) without making it a recommendation.

## 8. Test IDs

| Element | Test ID |
|---------|---------|
| Check-type radio "email" | `check-type-email` |
| Email address display on detail page | `email-check-address` |
| Copy address button | `email-check-copy-btn` |
| Admin nav link | `admin-email-inbox-link` |
| Inbox enabled toggle | `email-inbox-enabled` |
| Test connection button | `email-inbox-test-btn` |
| Sync now button | `email-inbox-sync-btn` |

## 9. Playwright

`web/dash0/tests/email-checks.spec.ts`:

1. Super-admin configures the inbox using a fake JMAP server (started in test fixture from spec 01).
2. Regular user creates an email check.
3. Test fixture sends an email via the fake JMAP server.
4. Assert the check detail page shows status UP.
5. Send `<token>+down@…` and assert status flips to DOWN within poll interval.

If running the fake JMAP server in CI is too heavy, gate this behind a `@e2e-email` tag and run the simpler unit-level UI tests in the default suite (form rendering, address rendering, copy button).

## 10. Files to create / modify

| File | Status |
|------|--------|
| `web/dash0/src/api/hooks.ts` | union types + helpers |
| `web/dash0/src/api/email-inbox.ts` | new — typed clients for status / test / sync / public param |
| `web/dash0/src/components/shared/check-form.tsx` | add `email` type |
| `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx` | "Email Endpoint" section |
| `web/dash0/src/routes/orgs/$org/checks.index.tsx` | target column rendering |
| `web/dash0/src/routes/admin/email-inbox.tsx` | new admin page |
| `web/dash0/src/components/admin/email-inbox-form.tsx` | new |
| `web/dash0/tests/email-checks.spec.ts` | new (e2e, gated) |
| `server/internal/handlers/system/handler.go` | add `GET /system/parameters/email_inbox/public` |

## 11. Verification

1. `make dev` — backend + dash0 hot reload.
2. As super-admin, visit `/admin/email-inbox`, fill creds for a JMAP server, save, click "Test Connection" → green toast.
3. As regular user, create an email check; copy its address.
4. Send an email to that address from any client; refresh check detail → status UP within ~5s (EventSource) or one poll interval (polling).
5. Send to `<token>+down@…` → status flips to DOWN.
6. Wait past `period`; without further emails, worker flips status to DOWN.
