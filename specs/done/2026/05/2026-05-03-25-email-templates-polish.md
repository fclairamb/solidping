# Email — polish transactional templates (especially invitation)

## Context

The transactional email templates in `server/internal/email/templates/` are functional but rough:

- `invitation.html` is **10 lines** with no greeting, no fallback URL, no security note, no help line.
- `base.html` uses `<div>`-based layout (not the table-based layout most email clients expect for reliable rendering), no responsive breakpoints, a hard-coded color palette, and a generic footer.
- Subjects are **hard-coded strings** at every call site (`"[SolidPing] You're invited to " + orgName` in `server/internal/handlers/auth/service.go:2381`, etc.) rather than living next to the template body. Changing wording means hunting through Go code.
- No "View this email in your browser" / fallback URL line under the CTA — when a mail client strips button hrefs (Outlook 2016, some webmail clients), recipients can't accept the invite.

The user explicitly called out invites as the priority. The infrastructure is fine — `TemplateFormatter` (`server/internal/email/formatter.go`) already does Go templates + premailer CSS inlining + plain-text fallback. The work here is **content + layout**, plus one small API change to colocate subject lines with templates.

## Honest opinion

The biggest single UX win is the **fallback URL** line under the CTA button, then better visual hierarchy and a cleaner footer. The mobile-responsive table layout matters less than people think for a 600px-max email, but it's standard hygiene.

For subjects, two options:

- **(a)** `{{define "subject"}}...{{end}}` block at the top of each template, executed with the same data. Keeps everything for one email in one file. Recommended.
- **(b)** Separate `subjects.go` map. More Go-idiomatic but splits one logical thing across two files.

I'm going with (a). It mirrors how mailers like ActionMailer handle this and means a non-developer editing copy only touches the `.html` file.

Out of scope on purpose: **i18n**. Realassets has `invitation.en.html` / `invitation.fr.html` and a separate `.txt` plain-text file per language. Solidping has a recent language-switcher spec on the frontend, so multi-language email is a natural next step — but it doubles the template count and needs locale resolution per recipient (which user model field? what default?). That's a separate, larger spec.

## Scope

**In:**
- Rewrite `base.html` with table-based layout, responsive `<style>` block, refined header/footer/button styles.
- Rewrite `invitation.html` with greeting, role/org context, prominent CTA, fallback URL line, expiration explanation, ignore-if-not-you note.
- Apply the same polish pattern to `registration.html`, `password-reset.html`, `welcome.html`.
- Add `{{define "subject"}}...{{end}}` block to each template.
- Extend `Formatter.Format` signature from `(html, text, error)` to `(subject, html, text, error)`.
- Update `EmailJobRun.buildMessage` (`server/internal/jobs/jobtypes/job_email.go:152`) to use the rendered subject when `EmailJobConfig.Subject` is empty (caller can still override).
- Update the `Format` interface contract in `server/internal/email/sender.go` (search for the `Formatter` interface — it's defined alongside `Sender`).
- Update the call site in `server/internal/notifications/email.go:70` (which still uses `Format` directly).
- Snapshot tests for `Format("invitation.html", sampleData)` asserting subject + presence of fallback URL in HTML and plain text.

**Out:**
- Localization (separate spec, see "Honest opinion").
- Email preview HTTP route — useful but optional; can follow up if the templates need iteration.
- `incident.html` — already used in production by notifications, has different shape, leave alone in this pass.
- Job-refactor work (`2026-05-03-16`) and header work (`2026-05-03-17`) — independent.

## Implementation

### 1. Updated `Formatter.Format` signature

```go
// Format renders a template with the given data.
// Returns subject (rendered from the template's {{define "subject"}} block),
// HTML (with inlined CSS), and plain text.
// If the template has no "subject" block, subject is returned as "".
func (f *TemplateFormatter) Format(templateName string, data any) (subject, html, text string, err error)
```

Inside `Format`, after parsing the template, also execute the `subject` block:

```go
var subjBuf bytes.Buffer
if t := tmpl.Lookup("subject"); t != nil {
    if err := t.Execute(&subjBuf, data); err != nil {
        return "", "", "", fmt.Errorf("executing subject for %s: %w", templateName, err)
    }
}
subject = strings.TrimSpace(subjBuf.String())
```

Update the `email.Formatter` interface declaration to match.

### 2. Subject use-when-empty in the email job

`server/internal/jobs/jobtypes/job_email.go:164` (template branch of `buildMessage`):

```go
subject, html, text, err := jctx.Services.EmailFormatter.Format(r.config.Template, r.config.TemplateData)
if err != nil {
    return nil, fmt.Errorf("formatting template %s: %w", r.config.Template, err)
}
msg.HTML = html
msg.Text = text
if msg.Subject == "" {
    msg.Subject = subject
}
```

`validateEmailConfig` (L68) currently requires a non-empty `Subject`. Relax that: subject is required only when no template is supplied. With a template, the subject can come from the template itself.

### 3. Template structure

Every template gains:

```html
{{define "subject"}}You're invited to {{.OrgName}}{{end}}
{{template "base.html" .}}
{{define "content"}}
  ...
{{end}}
```

Pick subject lines without the `[SolidPing]` prefix — the From-name change in spec `24` (`SolidPing <noreply@…>`) already gives the brand attribution. `[SolidPing] You're invited to Acme` reads spammy when From already says SolidPing.

Suggested subjects:
- `registration.html` → `Confirm your SolidPing account`
- `password-reset.html` → `Reset your SolidPing password`
- `invitation.html` → `You're invited to join {{.OrgName}}`
- `welcome.html` → `Welcome to {{.OrgName}}`

### 4. New `base.html` skeleton

Use a 600px max-width table layout, system font stack, single inline `<style>` block (premailer will inline what it can; `@media` queries it leaves alone). Sections: header (text logo "SolidPing"), `{{block "content" .}}{{end}}`, footer ("You received this because… If this wasn't you, ignore this email."). Avoid background images and web fonts — neither are reliable in email clients.

### 5. New `invitation.html` content (sketch)

```
Hi,

{{.InviterName}} invited you to join {{.OrgName}} on SolidPing as {{.Role}}.

[ Accept invitation ]   ← CTA button

Or paste this link into your browser:
{{.InviteURL}}

This invitation expires in 7 days.
If you weren't expecting this, you can safely ignore this email.
```

Apply equivalent structure (greeting → context → CTA → fallback URL → expiration → ignore note) to `registration.html`, `password-reset.html`, `welcome.html`.

### 6. Update other call sites

After signature change, fix every caller of `Formatter.Format`:
- `server/internal/notifications/email.go:70` — discard returned subject (notifications set their own).
- Any places spec `23` introduced (handle in whichever spec lands second; touch both files in that PR).

### 7. Snapshot tests

`server/internal/email/formatter_test.go`:

```go
func TestFormat_Invitation(t *testing.T) {
    t.Parallel()
    r := require.New(t)
    f, err := email.NewFormatter()
    r.NoError(err)
    subject, html, text, err := f.Format("invitation.html", map[string]any{
        "OrgName": "Acme", "Role": "admin",
        "InviterName": "Alice", "InviteURL": "https://example.com/i/abc",
    })
    r.NoError(err)
    r.Equal("You're invited to join Acme", subject)
    r.Contains(html, "https://example.com/i/abc") // CTA href
    r.Contains(text, "https://example.com/i/abc") // fallback URL also in plain text
    r.Contains(text, "expires in 7 days")
}
```

One per template. Keep them small and assertion-focused — these are sanity checks, not full visual regressions.

## Verification

- `make test` and `make lint` pass.
- Render each template by running the new tests with `-v` and saving the HTML output to `/tmp/`, then opening in a browser at 320px, 600px, and 1200px widths — check that header/footer/CTA look right at each.
- Run a real send through Mailpit (see spec `24` for setup); view in:
  - Mailpit's preview (Webkit rendering)
  - Gmail web client (forward the message to a real Gmail address)
  - Outlook — if available; if not, accept the residual risk
- Click the CTA button in each test email and confirm it lands on the right URL. Then copy-paste the fallback URL and confirm the same.

## Critical files

- `server/internal/email/formatter.go` — `Format` signature change, subject extraction.
- `server/internal/email/templates/base.html` — full rewrite.
- `server/internal/email/templates/invitation.html` — full rewrite.
- `server/internal/email/templates/registration.html` — polish + subject block.
- `server/internal/email/templates/password-reset.html` — polish + subject block.
- `server/internal/email/templates/welcome.html` — polish + subject block.
- `server/internal/email/sender.go` — `Formatter` interface declaration.
- `server/internal/jobs/jobtypes/job_email.go` — `validateEmailConfig` relaxation, `buildMessage` use-rendered-subject.
- `server/internal/notifications/email.go` — call-site update.
- `server/internal/email/formatter_test.go` — snapshot tests (new or extended).

## Implementation Plan

1. Extend the `email.Formatter` interface and `TemplateFormatter.Format` method to return `(subject, html, text, err)`. Subject comes from a `{{define "subject"}}` block on the template; empty string when none.
2. Update the existing call sites:
   - `internal/jobs/jobtypes/job_email.go` — use the rendered subject when `EmailJobConfig.Subject` is empty; relax `validateEmailConfig` so subject is required only when no template is supplied.
   - `internal/email/formatter_test.go` and `internal/email/sender_test.go` — adjust to the 4-return signature.
   - `internal/notifications/email.go` does NOT use Format directly (verified — it constructs HTML inline) so no change there.
3. Rewrite `templates/base.html` with a 600px-max table layout, system font stack, responsive `<style>` block, refined header / button / footer styles. Adds a "subject" placeholder convention — child templates define their own `{{define "subject"}}`.
4. Rewrite `templates/invitation.html`, `templates/registration.html`, `templates/password-reset.html`, `templates/welcome.html` with greeting + context + CTA + fallback URL + expiration + ignore-if-not-you note. Each gets a `{{define "subject"}}` block; subjects drop the `[SolidPing]` prefix (the From-name change in spec 24 already attributes the brand).
5. Update auth service callers to pass `Subject: ""` (template provides it) instead of hard-coding `[SolidPing] ...` strings.
6. Add subject + fallback-URL snapshot tests in `internal/email/formatter_test.go`, one per template.
7. Run `make fmt`, `make lint-back`, `make test`.
