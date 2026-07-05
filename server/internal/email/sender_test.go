package email

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wneessen/go-mail"

	"github.com/fclairamb/solidping/server/internal/config"
)

func TestSender_DisabledNoOp(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &config.EmailConfig{
		Enabled: false,
	}

	sender := NewSender(cfg, slog.Default())

	result, err := sender.Send(context.Background(), &Message{
		Recipients: Recipients{
			To: []string{"test@example.com"},
		},
		Subject: "Test",
		Text:    "Test message",
	})

	// Should return result without sending (no-op)
	r.NoError(err)
	r.NotNil(result)
	r.False(result.Sent)
	r.Contains(result.Message, "disabled")
}

func TestSender_NoRecipients(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &config.EmailConfig{
		Enabled: true,
		Host:    "localhost",
		Port:    587,
		From:    "test@example.com",
	}

	sender := NewSender(cfg, slog.Default())

	result, err := sender.Send(context.Background(), &Message{
		Recipients: Recipients{
			To: []string{}, // Empty recipients
		},
		Subject: "Test",
		Text:    "Test message",
	})

	r.Error(err)
	r.Nil(result)
	r.ErrorIs(err, ErrNoRecipients)
}

func TestBuildMessage_HeadersIdentitySolidPing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		fromName    string
		wantFromHas string
	}{
		{
			name:        "default from name when unset",
			fromName:    "",
			wantFromHas: `"` + defaultFromName + `" <`,
		},
		{
			name:        "custom from name preserved",
			fromName:    "Acme Status",
			wantFromHas: `"Acme Status" <`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			cfg := &config.EmailConfig{
				Enabled:  true,
				Host:     "localhost",
				Port:     587,
				From:     "noreply@example.com",
				FromName: tc.fromName,
			}

			sender := NewSender(cfg, slog.Default())
			mailMsg, err := sender.buildMessage(&Message{
				Recipients: Recipients{To: []string{"to@example.com"}},
				Subject:    "Hello",
				Text:       "Body",
			})
			r.NoError(err)

			xMailers := mailMsg.GetGenHeader(mail.HeaderXMailer)
			r.Len(xMailers, 1)
			r.True(strings.HasPrefix(xMailers[0], "SolidPing/"),
				"X-Mailer should start with SolidPing/, got %q", xMailers[0])
			r.NotContains(xMailers[0], "go-mail")

			froms := mailMsg.GetFromString()
			r.Len(froms, 1)
			r.Contains(froms[0], tc.wantFromHas)
			r.Contains(froms[0], "noreply@example.com")
		})
	}
}

func TestSendEmail_Integration(t *testing.T) {
	t.Parallel()

	if os.Getenv("SP_EMAIL_HOST") == "" {
		t.Skip("Skipping email integration test: SP_EMAIL_HOST not set")
	}

	r := require.New(t)

	port := 587
	if portStr := os.Getenv("SP_EMAIL_PORT"); portStr != "" {
		var err error
		port, err = strconv.Atoi(portStr)
		r.NoError(err)
	}

	cfg := &config.EmailConfig{
		Enabled:  true,
		Host:     os.Getenv("SP_EMAIL_HOST"),
		Port:     port,
		Username: os.Getenv("SP_EMAIL_USERNAME"),
		Password: os.Getenv("SP_EMAIL_PASSWORD"),
		From:     os.Getenv("SP_EMAIL_FROM"),
		FromName: os.Getenv("SP_EMAIL_FROMNAME"),
	}

	sender := NewSender(cfg, slog.Default())

	// Create a formatter for the test
	formatter, err := NewFormatter()
	r.NoError(err)

	data := map[string]any{
		"CheckName":   "Integration Test Check",
		"CheckType":   "http",
		"StartedAt":   "2026-07-05 10:00:00",
		"IncidentUID": "inc-integration-test",
		"IncidentURL": "https://solidping.com/dash0/orgs/default/incidents/inc-integration-test",
	}

	_, html, _, err := formatter.Format("incident-created.html", data)
	r.NoError(err)

	result, err := sender.Send(context.Background(), &Message{
		Recipients: Recipients{
			To: []string{cfg.From}, // Send to self for testing
		},
		Subject: "[SolidPing Test] Integration Test Email",
		HTML:    html,
	})
	r.NoError(err)
	r.NotNil(result)
	r.True(result.Sent)
	r.NotEmpty(result.MessageID, "Message-ID should be populated after a successful send")
}

// TestSetBody_MultipartOrdering is a regression guard for the Gmail rendering
// fix. Per RFC 2046 §5.1.4, multipart/alternative parts must be ordered from
// least to most preferred (preferred last). Spec-compliant readers (Gmail,
// Apple Mail) pick the LAST part they can render, so plaintext must come
// before HTML in the wire output — otherwise Gmail shows the lynx-style
// auto-text instead of our styled HTML.
func TestSetBody_MultipartOrdering(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &config.EmailConfig{
		Enabled: true,
		Host:    "localhost",
		Port:    587,
		From:    "noreply@example.com",
	}
	sender := NewSender(cfg, slog.Default())

	mailMsg, err := sender.buildMessage(&Message{
		Recipients: Recipients{To: []string{"to@example.com"}},
		Subject:    "Hello",
		HTML:       "<p>Hi there</p>",
		Text:       "Hi there",
	})
	r.NoError(err)

	var buf bytes.Buffer
	_, err = mailMsg.WriteTo(&buf)
	r.NoError(err)

	wire := buf.String()
	plainIdx := strings.Index(wire, "text/plain")
	htmlIdx := strings.Index(wire, "text/html")
	r.NotEqual(-1, plainIdx, "wire output should contain text/plain part")
	r.NotEqual(-1, htmlIdx, "wire output should contain text/html part")
	r.Less(plainIdx, htmlIdx,
		"text/plain must appear before text/html in multipart/alternative (RFC 2046 §5.1.4); "+
			"otherwise Gmail renders the plaintext instead of the styled HTML")
}

// TestSetBody_HTMLOnly verifies that supplying only HTML produces a single
// text/html part with no multipart wrapping (no auto-text fallback).
func TestSetBody_HTMLOnly(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &config.EmailConfig{
		Enabled: true,
		Host:    "localhost",
		Port:    587,
		From:    "noreply@example.com",
	}
	sender := NewSender(cfg, slog.Default())

	mailMsg, err := sender.buildMessage(&Message{
		Recipients: Recipients{To: []string{"to@example.com"}},
		Subject:    "Hello",
		HTML:       "<p>Hi there</p>",
	})
	r.NoError(err)

	var buf bytes.Buffer
	_, err = mailMsg.WriteTo(&buf)
	r.NoError(err)

	wire := buf.String()
	r.Contains(wire, "text/html")
	r.NotContains(wire, "text/plain",
		"HTML-only message must not include a plaintext alternative")
	r.NotContains(wire, "multipart/alternative",
		"HTML-only message must not be wrapped in multipart/alternative")
}

// TestBuildMessage_ListUnsubscribeHeaders pins the D4 List-Unsubscribe /
// List-Unsubscribe-Post wiring (spec 2026-07-05-10, acceptance criteria 4
// and 6): incident/alert emails that set ListUnsubscribeURL get both
// headers; a message that leaves it empty (every transactional email) gets
// neither header at all.
func TestBuildMessage_ListUnsubscribeHeaders(t *testing.T) {
	t.Parallel()

	cfg := &config.EmailConfig{
		Enabled: true, Host: "localhost", Port: 587, From: "noreply@example.com",
	}

	t.Run("incident email with unsubscribe URL gets both headers", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		sender := NewSender(cfg, slog.Default())

		mailMsg, err := sender.buildMessage(&Message{
			Recipients:                  Recipients{To: []string{"to@example.com"}},
			Subject:                     "Hello",
			HTML:                        "<p>Hi there</p>",
			ListUnsubscribeURL:          "https://solidping.example/unsubscribe?token=abc",
			ListUnsubscribePostOneClick: true,
		})
		r.NoError(err)

		var buf bytes.Buffer
		_, err = mailMsg.WriteTo(&buf)
		r.NoError(err)

		wire := buf.String()
		r.Contains(wire, "List-Unsubscribe: <https://solidping.example/unsubscribe?token=abc>")
		r.Contains(wire, "List-Unsubscribe-Post: List-Unsubscribe=One-Click")
	})

	t.Run("transactional email without unsubscribe URL gets no List-Unsubscribe headers", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		sender := NewSender(cfg, slog.Default())

		mailMsg, err := sender.buildMessage(&Message{
			Recipients: Recipients{To: []string{"to@example.com"}},
			Subject:    "Confirm your account",
			HTML:       "<p>Click to confirm</p>",
			// ListUnsubscribeURL deliberately left empty — this is the
			// shape every transactional send (registration, reset,
			// invitation, password-changed) uses.
		})
		r.NoError(err)

		var buf bytes.Buffer
		_, err = mailMsg.WriteTo(&buf)
		r.NoError(err)

		wire := buf.String()
		r.NotContains(wire, "List-Unsubscribe:",
			"transactional emails must never carry a List-Unsubscribe header")
		r.NotContains(wire, "List-Unsubscribe-Post:",
			"transactional emails must never carry a List-Unsubscribe-Post header")
	})

	t.Run("unsubscribe URL without one-click flag omits List-Unsubscribe-Post", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		sender := NewSender(cfg, slog.Default())

		mailMsg, err := sender.buildMessage(&Message{
			Recipients:         Recipients{To: []string{"to@example.com"}},
			Subject:            "Hello",
			HTML:               "<p>Hi there</p>",
			ListUnsubscribeURL: "https://solidping.example/unsubscribe?token=abc",
			// ListUnsubscribePostOneClick left false.
		})
		r.NoError(err)

		var buf bytes.Buffer
		_, err = mailMsg.WriteTo(&buf)
		r.NoError(err)

		wire := buf.String()
		r.Contains(wire, "List-Unsubscribe:")
		r.NotContains(wire, "List-Unsubscribe-Post:")
	})
}
