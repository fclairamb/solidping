package email

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

const testSupportMailbox = "support@acme.com"

func supportSenderConfig(replyTo string) *config.EmailConfig {
	return &config.EmailConfig{
		Enabled: true, Host: "localhost", Port: 587, From: "noreply@acme.com",
		ReplyTo: replyTo,
	}
}

// wireFor renders a message the way the SMTP sender would and returns the raw
// RFC 5322 bytes, which is where the headers actually have to appear.
func wireFor(t *testing.T, cfg *config.EmailConfig, msg *Message) string {
	t.Helper()

	r := require.New(t)
	sender := NewSender(cfg, slog.Default())

	mailMsg, err := sender.buildMessage(msg)
	r.NoError(err)

	var buf bytes.Buffer

	_, err = mailMsg.WriteTo(&buf)
	r.NoError(err)

	return buf.String()
}

// TestSupportReplyableClassification is the test that keeps the fail-closed
// design honest. Because the Reply-To is an OPT-IN, a new notification template
// that nobody classified silently loses the support notice — a quiet, invisible
// regression. Enumerating the shipped templates converts that into a build
// failure the moment the template file appears.
func TestSupportReplyableClassification(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	shipped, err := shippedTemplateNames()
	r.NoError(err)
	r.NotEmpty(shipped, "the embedded templates directory must not be empty")

	classified := classifiedTemplates()

	for _, name := range shipped {
		_, ok := classified[name]
		r.True(ok,
			"template %s has no support-Reply-To classification. Add it to "+
				"supportReplyableTemplates in supportreply.go: `true` for an alert or "+
				"notification a human may answer, `false` for security/identity mail "+
				"(password reset, password changed, registration, invitation, "+
				"double-opt-in confirmation).", name)
	}

	for name := range classified {
		r.Contains(shipped, name,
			"template %s is classified but no longer shipped — remove the stale entry", name)
	}

	// The security half of the partition, spelled out. These four are the whole
	// reason the mechanism is opt-in; if this list ever flips, the feature has
	// become a phishing aid.
	for _, name := range []string{
		"password-reset.html", "password-changed.html",
		"registration.html", "invitation.html", "status-subscriber-confirm.html",
	} {
		r.False(SupportReplyable(name), "%s must never carry a support Reply-To", name)
	}

	// Positive control: without these the test above would pass on a map whose
	// every value is false.
	for _, name := range []string{
		"incident-created.html", "incident-resolved.html", "escalation.html",
		"uptime-report.html", "welcome.html",
	} {
		r.True(SupportReplyable(name), "%s should carry the support Reply-To", name)
	}

	// An unknown template fails closed.
	r.False(SupportReplyable("some-future-template.html"))
	r.False(SupportReplyable(""))
}

func TestSupportReply_SecurityMailStaysClean(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := supportSenderConfig(testSupportMailbox)

	// This is the assertion that actually protects the security mail: the
	// instance HAS a support mailbox configured, and the password-reset mail
	// still carries neither the header nor the notice.
	for _, template := range []string{"password-reset.html", "password-changed.html"} {
		wire := wireFor(t, cfg, &Message{
			Recipients:       Recipients{To: []string{"user@acme.com"}},
			Subject:          "Reset your password",
			HTML:             "<html><body><p>Reset link</p></body></html>",
			Text:             "Reset link",
			SupportReplyable: SupportReplyable(template),
		})

		r.NotContains(wire, "Reply-To:", "%s must carry no Reply-To header", template)
		r.NotContains(wire, "You can reply directly to this email", "%s must carry no notice", template)
	}

	// Positive control on the very same config: an alert mail DOES get both, so
	// the assertions above cannot be passing because the feature is simply off.
	alert := wireFor(t, cfg, &Message{
		Recipients:       Recipients{To: []string{"user@acme.com"}},
		Subject:          "Incident",
		HTML:             "<html><body><p>Down</p></body></html>",
		Text:             "Down",
		SupportReplyable: SupportReplyable("incident-created.html"),
	})
	r.Contains(alert, "Reply-To:")
	r.Contains(alert, testSupportMailbox)
}

func TestSupportReply_OffWhenUnconfigured(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// SP_EMAIL_REPLY_TO unset: the feature is genuinely off, not defaulted on.
	wire := wireFor(t, supportSenderConfig(""), &Message{
		Recipients:       Recipients{To: []string{"user@acme.com"}},
		Subject:          "Incident",
		HTML:             "<html><body><p>Down</p></body></html>",
		Text:             "Down",
		SupportReplyable: true,
	})

	r.NotContains(wire, "Reply-To:")
	r.NotContains(wire, "You can reply directly to this email")
}

func TestSupportReply_ExplicitReplyToWins(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	wire := wireFor(t, supportSenderConfig(testSupportMailbox), &Message{
		Recipients: Recipients{
			To:      []string{"user@acme.com"},
			ReplyTo: "incident-42@acme.com",
		},
		Subject:          "Incident",
		HTML:             "<html><body><p>Down</p></body></html>",
		Text:             "Down",
		SupportReplyable: true,
	})

	r.Contains(wire, "incident-42@acme.com")
	r.NotContains(wire, "Reply-To: <"+testSupportMailbox+">")
}

func TestSupportReply_NoticeInBothBodies(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	msg := &Message{
		Recipients:       Recipients{To: []string{"user@acme.com"}},
		Subject:          "Incident",
		HTML:             "<html><body><p>Down</p></body></html>",
		Text:             "Down",
		SupportReplyable: true,
	}

	sender := NewSender(supportSenderConfig(testSupportMailbox), slog.Default())
	_, err := sender.buildMessage(msg)
	r.NoError(err)

	// Asserted on the struct, not the wire, because quoted-printable encoding
	// can split a long line and hide a substring that is genuinely present.
	r.Contains(msg.Text, SupportReplyNoticeText)
	r.Contains(msg.HTML, "You can reply directly to this email")
	// It landed inside the document, not after </body>.
	r.True(strings.Index(msg.HTML, "You can reply directly") < strings.Index(msg.HTML, "</body>"))
}

func TestSupportReply_IsIdempotent(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	msg := &Message{
		Recipients:       Recipients{To: []string{"user@acme.com"}},
		HTML:             "<html><body><p>Down</p></body></html>",
		Text:             "Down",
		SupportReplyable: true,
	}

	applySupportReply(msg, testSupportMailbox)
	applySupportReply(msg, testSupportMailbox)

	r.Equal(1, strings.Count(msg.Text, SupportReplyNoticeText))
	r.Equal(1, strings.Count(msg.HTML, "You can reply directly to this email"))
}

func TestAutomationHeaders(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := supportSenderConfig(testSupportMailbox)

	mirror := wireFor(t, cfg, &Message{
		Recipients:    Recipients{To: []string{"ops@acme.com"}},
		Subject:       "New support message",
		Text:          "someone said hi",
		AutoSubmitted: true,
		SupportMirror: true,
		// A mirror is explicitly NOT support-replyable: pointing it at the
		// support address would make it reply to itself.
		SupportReplyable: false,
	})

	r.Contains(mirror, "Auto-Submitted: auto-generated")
	r.Contains(mirror, HeaderSupportMirror+": 1")
	r.NotContains(mirror, "You can reply directly to this email")

	// Ordinary mail carries neither marker — otherwise the future inbound-email
	// capture would skip everything.
	plain := wireFor(t, cfg, &Message{
		Recipients: Recipients{To: []string{"user@acme.com"}},
		Subject:    "Incident",
		Text:       "Down",
	})
	r.NotContains(plain, "Auto-Submitted:")
	r.NotContains(plain, HeaderSupportMirror)
}
