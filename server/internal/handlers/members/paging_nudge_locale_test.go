package members_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestSendPagingNudgeSpeaksTheMembersLanguage: the nudge is written in the
// language of the member it goes to, not the admin who sent it.
func TestSendPagingNudgeSpeaksTheMembersLanguage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMemberFixture(ctx, t, "nudge-locale")

	user := models.NewUser("kai@acme.test")
	user.Preferences = &models.UserPreferences{Locale: "de", Timezone: "Europe/Berlin"}
	r.NoError(fx.dbSvc.CreateUser(ctx, user))

	member := models.NewOrganizationMember(fx.org.UID, user.UID, models.MemberRoleUser)
	r.NoError(fx.dbSvc.CreateOrganizationMember(ctx, member))

	r.NoError(fx.svc.SendPagingNudge(ctx, fx.org.Slug, member.UID))

	msg := fx.mailer.last
	r.Equal("Richten Sie Ihre Benachrichtigungen für Coverage Test Org ein", msg.Subject)
	r.Contains(msg.Text, "Hallo,\n\nEin Administrator von Coverage Test Org bittet Sie")
	r.Contains(msg.HTML, "<strong>Coverage Test Org</strong>")
	r.Contains(msg.HTML, "Kontakt hinzufügen und verifizieren")
	r.Equal("de", msg.Locale)
	r.Equal("Europe/Berlin", msg.Timezone)
}
