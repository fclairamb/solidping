package email

import (
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
		"CheckName":    "Integration Test Check",
		"Status":       "down",
		"Message":      "This is an integration test email.",
		"DashboardURL": "https://solidping.com/dashboard",
	}

	_, html, text, err := formatter.Format("incident.html", data)
	r.NoError(err)

	result, err := sender.Send(context.Background(), &Message{
		Recipients: Recipients{
			To: []string{cfg.From}, // Send to self for testing
		},
		Subject: "[SolidPing Test] Integration Test Email",
		HTML:    html,
		Text:    text,
	})
	r.NoError(err)
	r.NotNil(result)
	r.True(result.Sent)
}
