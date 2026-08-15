package system

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/email"
)

// capturingSender is a fake email.Sender that records the last message
// instead of dialing SMTP, so TestEmail's rendering can be asserted without a
// live mail server.
type capturingSender struct {
	last *email.Message
}

func (c *capturingSender) Send(_ context.Context, msg *email.Message) (*email.SendResult, error) {
	c.last = msg

	return &email.SendResult{Sent: true, MessageID: "test-msg-id"}, nil
}

// newTestEmailEnv seeds an in-memory DB with the minimal SMTP parameters
// TestEmail needs to get past its Enabled/From guards.
func newTestEmailEnv(t *testing.T) *sqlite.Service {
	t.Helper()

	ctx := context.Background()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	r.NoError(dbSvc.SetSystemParameter(ctx, "email.enabled", true, false))
	r.NoError(dbSvc.SetSystemParameter(ctx, "email.from", "noreply@example.com", false))

	return dbSvc
}

// TestTestEmail_RendersThroughFormatter proves the admin "test email" (site 2
// of the email-formatter migration) renders through test-email.html instead
// of the old hand-rolled `<h2>...</h2><p>...</p>` string. The negative
// control is the point: a test that only checks "non-empty" would pass
// against the old code too.
//
//nolint:paralleltest // mutates the package-level newEmailSender seam.
func TestTestEmail_RendersThroughFormatter(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	dbSvc := newTestEmailEnv(t)

	formatter, err := email.NewFormatter()
	r.NoError(err)

	svc := NewService(dbSvc)
	svc.SetEmailFormatter(formatter)

	captured := &capturingSender{}
	prev := newEmailSender
	newEmailSender = func(_ *config.EmailConfig, _ *slog.Logger) email.Sender { return captured }
	t.Cleanup(func() { newEmailSender = prev })

	resp, err := svc.TestEmail(ctx, "someone@example.com")
	r.NoError(err)
	r.NotNil(resp)
	r.True(resp.Sent)

	r.NotNil(captured.last)
	msg := captured.last

	r.Equal([]string{"someone@example.com"}, msg.Recipients.To)
	r.Equal("SolidPing Test Email", msg.Subject)
	r.NotEmpty(msg.HTML, "must have an HTML part")
	r.NotEmpty(msg.Text, "must have a text part")
	r.Contains(msg.HTML, "SolidPing Test Email")
	r.Contains(msg.HTML, "configuration is working correctly")

	// Negative control: the pre-migration body was exactly
	// "<h2>SolidPing Test Email</h2><p>...</p>", a bare fragment with no
	// base.html wrapper. Confirm that marker is gone and the shared branding
	// (only present via base.html) is there instead.
	r.NotContains(msg.HTML, "<h2>SolidPing Test Email</h2>")
	r.Contains(msg.HTML, "SolidPing</span>", "must include the shared base.html header branding")
}

// TestTestEmail_NilFormatterGuard proves the nil-formatter guard added when
// TestEmail was migrated to the shared formatter is real, not just assumed:
// with no formatter wired, TestEmail must answer a clear "not configured"
// result rather than panic or silently attempt to send.
func TestTestEmail_NilFormatterGuard(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	dbSvc := newTestEmailEnv(t)

	svc := NewService(dbSvc) // formatter never wired

	resp, err := svc.TestEmail(ctx, "someone@example.com")
	r.NoError(err)
	r.NotNil(resp)
	r.False(resp.Sent)
	r.Contains(resp.Message, "formatter")
}
