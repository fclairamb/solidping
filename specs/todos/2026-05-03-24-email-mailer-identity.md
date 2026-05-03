# Email — identify outgoing mail as SolidPing, not "go-mail"

## Context

Every email SolidPing sends today carries an `X-Mailer` header advertising the underlying SMTP library:

```
X-Mailer: go-mail v0.7.2 // https://github.com/wneessen/go-mail
```

This is auto-stamped by `github.com/wneessen/go-mail` whenever a message is built (confirmed in `~/go/pkg/mod/github.com/wneessen/go-mail@v0.7.2/header.go:90` and visible in the library's own `testdata/invoice.eml`). It leaks the implementation detail to recipients, looks unprofessional in mail-client "view source" / spam-analysis tooling, and pins our brand to a third-party library name.

Separately, the `From` display name handling in `server/internal/email/sender.go` `setFrom()` (L77–89) falls through to bare-address `From()` when `EmailConfig.FromName` is empty. Mail clients then render the bare address (e.g. `noreply@solidping.example.com`) where they could be rendering `SolidPing <noreply@solidping.example.com>`. Operators who deploy without setting `email.from_name` get the ugly version by default.

## Honest opinion

A two-line change with a real cosmetic + brand benefit. The X-Mailer override is unambiguously a fix; the FromName default is more of a quality-of-life nudge for self-hosters. I'm including both because it's the same file and the same review.

I considered using `mail.WithUserAgent(...)` at the client level instead of per-message — but go-mail's option is named differently and the per-message `SetGenHeader` is the cleanest single point of control. We get one place to change the string and one place to test it.

## Scope

**In:**
- Override `X-Mailer` header on every outgoing message with `SolidPing/<version>`.
- When `EmailConfig.FromName` is empty, default the display name to `"SolidPing"` instead of falling through to bare-address.
- Unit test confirming the rendered EML carries the expected `X-Mailer` and `From` headers.

**Out:**
- Routing changes (covered in `2026-05-03-23-email-job-refactor.md`).
- Template content (covered in `2026-05-03-25-email-templates-polish.md`).
- Changing the actual `From` address (still operator-controlled via `email.from`).
- DKIM / SPF / DMARC — orthogonal concern, operator's responsibility on the SMTP relay.

## Implementation

### 1. X-Mailer override

`server/internal/email/sender.go` `buildMessage()` (currently L58–74):

```go
func (s *SMTPSender) buildMessage(msg *Message) (*mail.Msg, error) {
    mailMsg := mail.NewMsg()

    // Identify ourselves; otherwise go-mail stamps its own X-Mailer.
    mailMsg.SetGenHeader(mail.HeaderXMailer, "SolidPing/"+version.Version)

    if err := s.setFrom(mailMsg); err != nil {
        return nil, err
    }
    // ... unchanged
}
```

Add imports: `mail.HeaderXMailer` is already in `github.com/wneessen/go-mail`; add `"github.com/fclairamb/solidping/server/internal/version"`.

### 2. Default FromName

`server/internal/email/sender.go` `setFrom()` (L77–89):

```go
func (s *SMTPSender) setFrom(mailMsg *mail.Msg) error {
    name := s.config.FromName
    if name == "" {
        name = "SolidPing"
    }
    if err := mailMsg.FromFormat(name, s.config.From); err != nil {
        return fmt.Errorf("setting from address: %w", err)
    }
    return nil
}
```

(The `else` branch with bare `From()` becomes unreachable and goes away.)

### 3. Tests

New test in `server/internal/email/sender_test.go` (create the file if it doesn't exist):

- Build an `SMTPSender` with a minimal `EmailConfig` (no FromName).
- Call `buildMessage(&Message{...})`.
- Use `mailMsg.GetGenHeader(mail.HeaderXMailer)` to assert the value matches `^SolidPing/.+`.
- Use `mailMsg.GetGenHeader(mail.HeaderFrom)` to assert it contains `SolidPing <`.
- Add a second case with `FromName: "Acme Status"` — assert `From` contains `Acme Status <`.

Use `testify/require` and `t.Parallel()` per project conventions.

## Verification

- `make test` and `make lint` pass.
- Manual: with a local Mailpit (`docker run -p 1025:1025 -p 8025:8025 axllent/mailpit`) configured as the SMTP relay, send any email (e.g. trigger a test from `/api/v1/system/test-email` if available, or register a user). Open the message in Mailpit's UI → "Source" → confirm:
  ```
  X-Mailer: SolidPing/<version>
  From: SolidPing <whatever-from-address>
  ```
- No remaining `go-mail` reference in the headers.

## Critical files

- `server/internal/email/sender.go` — `buildMessage`, `setFrom`.
- `server/internal/version/version.go` — `Version` package variable.
- `server/internal/email/sender_test.go` — new file.
