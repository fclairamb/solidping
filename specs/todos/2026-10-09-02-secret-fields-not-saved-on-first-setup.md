---
model: sonnet
effort: medium
---

# Secret fields are silently dropped on first setup (SMTP password, JWT secret, inbox password)

*Reported by **Jonathan**, a self-hoster (Unraid template, then Docker Compose), on
2026-10-08: "I've configured my SMTP host (Brevo) but when I send the test mail I
get an Authentication Failed error message. I've verified my credentials are
working and they are being used on numerous other services."*

## What is wrong

`web/dash0/src/routes/orgs/$org/server.mail.tsx` only sends `email.password`
when `editingPassword` is true:

```tsx
...(editingPassword
  ? [setParam.mutateAsync({ key: "email.password", value: password, secret: true })]
  : []),
```

`editingPassword` is only set by the **Edit** button, and that button only
renders when a secret `email.password` row already exists
(`!editingPassword && isPasswordSecret`). On a fresh install there is no row, so:

1. The form shows a plain editable password input.
2. The user types the SMTP key and clicks Save.
3. Every other field is saved, the password is skipped, "Saved" is shown.
4. After the reload the password field is empty again.
5. The test mail logs in with the right username and an **empty password**. The
   SMTP server answers `535 Authentication failed`.

Nothing tells the user the password was dropped. Their credentials are correct.

The same gate exists on two more pages:

| Page | Field | Gate |
|---|---|---|
| `server.mail.tsx` | `email.password` | `editingPassword` |
| `server.web.tsx` | `auth.jwt_secret` | `editingJwt` |
| `server.email-inbox.tsx` | inbox `password` | `editingPassword && password` |

`server.slack.tsx` and `server.auth.tsx` are already correct: they treat the
input as editable when nothing is stored yet (`tokenInputVisible = editingToken
|| !tokenStored`, `!isSecretStored(field.key)`). Copy that pattern.

## Second problem: the test mail does not test what alerts use

- `POST /api/v1/system/test-email` (`server/internal/handlers/system/service.go`,
  `buildEmailConfig`) reads the email settings from the database only.
- Real notifications use `cfg.Email`, loaded once at startup with env over DB
  (`systemconfig.go`, `app/server.go`).

So:
- A user who sets `SP_EMAIL_PASSWORD` in Docker Compose and the rest in the UI
  gets a failing test mail while real alerts would work.
- A user who fixes the settings in the UI gets a passing test mail while real
  alerts keep the old settings until a restart.

## Proposal

1. In the three pages above, send the secret when the user is editing **or**
   when no secret is stored yet and the input is non-empty. One shared helper
   (`secretInputVisible(editing, stored)`) is fine. Do not send an empty value
   on first setup (that would store an empty secret).
2. Trim leading and trailing whitespace on host, username and from in the mail
   form. Do **not** trim the password.
3. Make the test mail use the same effective config as the real sender: env
   over DB, read fresh at send time. Either the sender re-reads the merged
   config on each send, or saving an `email.*` parameter rebuilds the sender.
   Pick the smaller change; the requirement is that test mail and alerts can
   never disagree.
4. Show `envOverrides` on the mail page, as `server.analytics.tsx` already
   does. A field set by `SP_EMAIL_*` is shown read-only with a note naming the
   env var.
5. When the SMTP server answers 535, make the error say so plainly:
   "The SMTP server rejected the username or password (535)". Keep the raw
   server text after it.

## Acceptance criteria

- [ ] Fresh database: fill the mail form including the password, click Save,
      reload. The password shows as stored (`******` + Edit).
- [ ] Same for the JWT secret on the web page and the inbox password.
- [ ] A Playwright test covers the first-save case for the mail page (the
      current `server-admin.spec.ts` only checks the test section is visible).
- [ ] Test mail with `SP_EMAIL_PASSWORD` set and no DB password succeeds.
- [ ] Changing the SMTP host in the UI changes the host real alerts use,
      without a restart.
- [ ] Env-overridden fields are visible as such on the mail page.
- [ ] Translations exist in en, fr, de, es.

## Reproduce

`SP_RUNMODE=test`, SQLite, fresh DB. Configure any SMTP relay that needs auth
(a free Brevo account works: `smtp-relay.brevo.com:587`, STARTTLS, login +
SMTP key). Save, send a test mail: 535. Then
`SELECT key FROM parameters WHERE key = 'email.password'` returns nothing.

Workaround for users on current releases: save once, reload, click **Edit**
next to the password, type it again, save.
