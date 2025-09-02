# Email System Specification

Create an email package for formatting and sending emails.

## Package Structure

```
back/internal/email/
├── email.go          # Main types and interfaces
├── sender.go         # SMTP sending implementation
├── sender_test.go    # Sender tests
├── formatter.go      # Template formatting with CSS inlining
├── formatter_test.go # Formatter tests
└── templates/        # Embedded HTML templates
    ├── base.html     # Base layout with CSS
    └── incident.html # Incident notification template
```

## Dependencies

```go
// go.mod additions
github.com/wneessen/go-mail      // SMTP client with TLS and context support
github.com/vanng822/go-premailer // CSS inlining for email client compatibility
```

## Configuration

Add to `internal/config/config.go`:

```go
type Config struct {
    // ... existing fields
    Email EmailConfig `koanf:"email"`
}

type EmailConfig struct {
    Host     string `koanf:"host"`     // SMTP server hostname
    Port     int    `koanf:"port"`     // SMTP port (typically 587 for STARTTLS)
    Username string `koanf:"username"` // SMTP username
    Password string `koanf:"password"` // SMTP password
    From     string `koanf:"from"`     // Default sender address
    FromName string `koanf:"fromname"` // Display name for sender
    Enabled  bool   `koanf:"enabled"`  // Enable/disable email sending
}
```

### Environment Variables

Following the project convention with `SP_` prefix:
- `SP_EMAIL_HOST` - SMTP server hostname
- `SP_EMAIL_PORT` - SMTP port (default: 587)
- `SP_EMAIL_USERNAME` - SMTP username
- `SP_EMAIL_PASSWORD` - SMTP password
- `SP_EMAIL_FROM` - Sender email address
- `SP_EMAIL_FROMNAME` - Sender display name (e.g., "SolidPing Alerts")
- `SP_EMAIL_ENABLED` - Enable email sending (default: false)

### Test Credentials (dev only)

```bash
SP_EMAIL_HOST=mail.k8xp.com
SP_EMAIL_PORT=587
SP_EMAIL_USERNAME=admin
SP_EMAIL_PASSWORD=changeme-admin-password-k8xp
SP_EMAIL_FROM=noreply@k8xp.com
SP_EMAIL_FROMNAME="SolidPing Alerts"
SP_EMAIL_ENABLED=true
SP_EMAIL_INSECURESKIPVERIFY=true
```

## Types and Interfaces

```go
package email

import "context"

// Recipients holds email recipient addresses.
type Recipients struct {
    To      []string // Primary recipients (required)
    CC      []string // Carbon copy recipients (optional)
    BCC     []string // Blind carbon copy recipients (optional)
    ReplyTo string   // Reply-to address (optional, defaults to From)
}

// Message represents an email to send.
type Message struct {
    Recipients Recipients // Email recipients
    Subject    string     // Email subject
    HTML       string     // HTML body
    Text       string     // Plain text body (fallback)
}

// Sender handles email delivery.
type Sender interface {
    // Send delivers an email. Returns nil immediately if email is disabled (no-op).
    // Returns error if sending fails.
    Send(ctx context.Context, msg *Message) error
}

// Formatter renders email templates.
type Formatter interface {
    // Format renders a template with the given data.
    // Returns both HTML (with inlined CSS) and plain text versions.
    Format(templateName string, data any) (html string, text string, err error)
}
```

## Implementation Details

### Sender (`sender.go`)

Uses `github.com/wneessen/go-mail` for robust SMTP handling:

```go
type SMTPSender struct {
    config  *config.EmailConfig
    logger  *slog.Logger
}

func NewSender(cfg *config.EmailConfig, logger *slog.Logger) *SMTPSender

// Send returns nil immediately if cfg.Enabled is false (no-op behavior)
func (s *SMTPSender) Send(ctx context.Context, msg *Message) error
```

Features:
- STARTTLS encryption (TLSOpportunistic by default)
- Context support for timeouts/cancellation
- Proper CC/BCC/ReplyTo handling
- Logging of send attempts and failures

### Formatter (`formatter.go`)

Uses `github.com/vanng822/go-premailer` for CSS inlining:

```go
//go:embed templates/*
var templateFS embed.FS

type TemplateFormatter struct {
    templates *template.Template
}

func NewFormatter() (*TemplateFormatter, error)

// Format renders template and inlines CSS from <style> tags into inline styles
func (f *TemplateFormatter) Format(templateName string, data any) (html, text string, err error)
```

The CSS inlining step transforms:
```html
<style>.button { background: blue; }</style>
<a class="button">Click</a>
```
Into:
```html
<a class="button" style="background: blue;">Click</a>
```

### Template Structure

Templates use Go's `{{block}}` and `{{define}}` for inheritance:

**Base template** (`templates/base.html`):
```html
<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <style>
        /* CSS here will be inlined by premailer */
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; }
        .container { max-width: 600px; margin: 0 auto; }
        .header { background: #1a1a2e; color: white; padding: 20px; text-align: center; }
        .content { padding: 20px; background: #ffffff; }
        .footer { padding: 20px; color: #666; font-size: 12px; text-align: center; }
        .button { display: inline-block; padding: 12px 24px; background: #dc3545; color: white; text-decoration: none; border-radius: 4px; }
        .status-down { color: #dc3545; font-weight: bold; }
        .status-up { color: #28a745; font-weight: bold; }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>SolidPing</h1>
        </div>
        {{block "content" .}}{{end}}
        <div class="footer">
            <p>This is an automated message from SolidPing.</p>
        </div>
    </div>
</body>
</html>
```

**Child template** (`templates/incident.html`):
```html
{{template "base.html" .}}
{{define "content"}}
<div class="content">
    <h2>Check Alert: {{.CheckName}}</h2>
    <p>Status: <span class="status-{{.Status}}">{{.Status | upper}}</span></p>
    <p>{{.Message}}</p>
    <p><a href="{{.DashboardURL}}" class="button">View Dashboard</a></p>
</div>
{{end}}
```

**How it works:**
1. `{{template "base.html" .}}` loads and executes base.html with current data
2. `{{block "content" .}}{{end}}` in base.html defines a placeholder
3. `{{define "content"}}...{{end}}` in child template fills that placeholder

## Testing

### Unit Tests

- Template rendering tests with sample data
- CSS inlining verification
- Error handling tests (invalid templates)

### Integration Test

```go
func TestSendEmail_Integration(t *testing.T) {
    if os.Getenv("SP_EMAIL_HOST") == "" {
        t.Skip("Skipping email integration test: SP_EMAIL_HOST not set")
    }
    // Test with real SMTP server
}
```

## Integration Points

The email package will be used by:

1. **Incident notifications** (`internal/handlers/incidents/`) - Send alerts when checks fail
2. **User management** (`internal/handlers/users/`) - Password reset, invitations
3. **Escalation system** (future) - Tiered alerting

### Service Registration

Add to `internal/app/services/services.go`:

```go
type ServicesList struct {
    // ... existing services
    EmailSender    email.Sender
    EmailFormatter email.Formatter
}
```

## Implementation Tasks

1. [ ] Add `EmailConfig` to `internal/config/config.go`
2. [ ] Add dependencies: `go-mail` and `go-premailer`
3. [ ] Create `internal/email/` package with types
4. [ ] Implement `Sender` using go-mail
5. [ ] Implement `Formatter` with premailer CSS inlining
6. [ ] Create base.html template
7. [ ] Create incident.html template
8. [ ] Add unit tests for formatter
9. [ ] Register email services in `services.go`
10. [ ] Add integration test (skipped by default)
