package system

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// TestTestEmail_SpeaksTheRecipientsLanguage: the SMTP test mail is written in
// the language of the account behind the address, else in the acting super
// admin's, else in English.
//
//nolint:paralleltest // mutates the package-level newEmailSender seam.
func TestTestEmail_SpeaksTheRecipientsLanguage(t *testing.T) {
	r := require.New(t)

	dbSvc := newTestEmailEnv(t)

	lea := models.NewUser("lea@acme.com")
	lea.Preferences = &models.UserPreferences{Locale: "fr", Timezone: "Europe/Paris"}
	r.NoError(dbSvc.CreateUser(t.Context(), lea))

	formatter, err := email.NewFormatter()
	r.NoError(err)

	svc := NewService(dbSvc)
	svc.SetEmailFormatter(formatter)

	captured := &capturingSender{}
	prev := newEmailSender
	newEmailSender = func(_ *config.EmailConfig, _ *slog.Logger) email.Sender { return captured }
	t.Cleanup(func() { newEmailSender = prev })

	actor := &models.User{Email: "root@acme.com", Preferences: &models.UserPreferences{Locale: "de"}}
	ctx := context.WithValue(t.Context(), base.ContextKeyUser, actor)

	_, err = svc.TestEmail(ctx, "LEA@acme.com")
	r.NoError(err)
	r.Equal("E-mail de test SolidPing", captured.last.Subject, "the account behind the address wins")
	r.Contains(captured.last.Text, "votre configuration e-mail fonctionne correctement")
	r.Equal("fr", captured.last.Locale)
	r.Equal("Europe/Paris", captured.last.Timezone)

	_, err = svc.TestEmail(ctx, "ops@acme.com")
	r.NoError(err)
	r.Equal("SolidPing-Test-E-Mail", captured.last.Subject, "no account: the acting super admin's language")
	r.Equal("de", captured.last.Locale)

	_, err = svc.TestEmail(t.Context(), "ops@acme.com")
	r.NoError(err)
	r.Equal("SolidPing Test Email", captured.last.Subject)
	r.Equal(i18n.LangEnglish, captured.last.Locale)
}
