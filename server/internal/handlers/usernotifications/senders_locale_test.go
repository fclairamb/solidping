package usernotifications

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// TestEmailSenderAdapter_SendTestEmail_SpeaksTheActingUsersLanguage: the
// route under test is the acting user's own, so the mail is in their language
// whatever address the contact holds; without a user it is English.
func TestEmailSenderAdapter_SendTestEmail_SpeaksTheActingUsersLanguage(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := email.NewFormatter()
	r.NoError(err)

	sender := &capturingSender{}
	adapter := NewEmailSenderAdapter(sender, formatter)

	user := &models.User{
		Email:       "ana@acme.com",
		Preferences: &models.UserPreferences{Locale: "es", Timezone: "Europe/Madrid"},
	}
	ctx := context.WithValue(t.Context(), base.ContextKeyUser, user)

	r.NoError(adapter.SendTestEmail(ctx, "pager@acme.com"))
	r.Equal([]string{"pager@acme.com"}, sender.last.Recipients.To)
	r.Equal("Notificación de prueba de SolidPing", sender.last.Subject)
	r.Contains(sender.last.Text, "El envío de tus correos funciona correctamente.")
	r.Equal("es", sender.last.Locale)
	r.Equal("Europe/Madrid", sender.last.Timezone)

	r.NoError(adapter.SendTestEmail(t.Context(), "pager@acme.com"))
	r.Equal("Test notification from SolidPing", sender.last.Subject)
	r.Equal(i18n.LangEnglish, sender.last.Locale)
}

// TestTestEmailData_EnglishIsUnchanged pins the English copy of both test
// sends to what they said before localization.
func TestTestEmailData_EnglishIsUnchanged(t *testing.T) {
	t.Parallel()

	require.Equal(t, map[string]any{
		"Subject": "Test notification from SolidPing",
		"Heading": "Test notification from SolidPing",
		"Body":    "This is a test notification from SolidPing. Your email delivery is working correctly.",
	}, TestEmailData(i18n.English))
}
