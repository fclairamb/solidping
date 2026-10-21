package aichecks_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/aichecks"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// sentMail is one mail a fakeMailer was handed.
type sentMail struct {
	orgUID, lang, timezone, subject, text string
	to                                    []string
}

// fakeMailer records the mails instead of queueing them.
type fakeMailer struct {
	mu    sync.Mutex
	mails []sentMail
}

func (m *fakeMailer) Mail(
	_ context.Context, orgUID string, to []string, loc i18n.Locale, subject, text string,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.mails = append(m.mails, sentMail{
		orgUID: orgUID, to: to, lang: loc.Lang(), timezone: loc.Timezone(), subject: subject, text: text,
	})

	return nil
}

// TestAppliedRepairIsMailedInTheOwnersLanguage: an applied repair mails the
// author of the version it replaced, in their language and zone, reason
// included; the reason stored on the version stays English.
func TestAppliedRepairIsMailedInTheOwnersLanguage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	tg := newTarget(t)
	fx := newFixture(t, always(fixedScript), nil)
	r.NoError(fx.db.UpdateUser(fx.ctx, fx.user.UID, &models.UserUpdate{
		Preferences: &models.UserPreferences{Locale: "fr", Timezone: "Europe/Paris"},
	}))

	mailer := &fakeMailer{}
	fx.svc.SetMailer(mailer)
	fx.svc.SetBaseURL("https://solidping.acme.com/")

	check := fx.createAICheck(t, tg.srv.URL, "auto")
	fx.driftResults(t, check.UID, 3, driftOutput())

	outcome, err := fx.svc.Repair(fx.ctx, fx.org.UID, check.UID)
	r.NoError(err)
	r.Equal(aichecks.OutcomeApplied, outcome.Outcome, outcome.Reason)
	r.Equal("AI repair: the script drifted at step parse", outcome.Reason, "the stored reason stays English")

	r.Len(mailer.mails, 1)
	mail := mailer.mails[0]
	r.Equal(fx.org.UID, mail.orgUID)
	r.Equal([]string{fx.user.Email}, mail.to)
	r.Equal("fr", mail.lang)
	r.Equal("Europe/Paris", mail.timezone)
	r.Equal("Vérification réparée : acme projects", mail.subject)
	r.Contains(mail.text, "SolidPing a réparé le script de la vérification « acme projects »")
	r.Contains(mail.text, "\n\nRéparation par IA : le script a dérivé à l'étape parse\n\n")
	r.Contains(mail.text, "+ if (!data.items)")
	r.Contains(mail.text, "Pour relire ou annuler cette réparation : https://solidping.acme.com/d/orgs/acme/checks/"+
		check.UID+"/history\n")
}

// TestAppliedRepairMailsAnEnglishOwnerInEnglish: no preference, the mail is
// the English one, in UTC.
func TestAppliedRepairMailsAnEnglishOwnerInEnglish(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	tg := newTarget(t)
	fx := newFixture(t, always(fixedScript), nil)

	mailer := &fakeMailer{}
	fx.svc.SetMailer(mailer)

	check := fx.createAICheck(t, tg.srv.URL, "auto")
	fx.driftResults(t, check.UID, 3, driftOutput())

	outcome, err := fx.svc.Repair(fx.ctx, fx.org.UID, check.UID)
	r.NoError(err)
	r.Equal(aichecks.OutcomeApplied, outcome.Outcome, outcome.Reason)

	r.Len(mailer.mails, 1)
	r.Equal("en", mailer.mails[0].lang)
	r.Equal("UTC", mailer.mails[0].timezone)
	r.Equal("Check repaired: acme projects", mailer.mails[0].subject)
	r.Contains(mailer.mails[0].text, "\n\nAI repair: the script drifted at step parse\n\n")
}
