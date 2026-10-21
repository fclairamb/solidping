package aichecks

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

const (
	repairReasonFixture = "AI repair: the script drifted: selector #price not found"
	repairDiffFixture   = "- await page.click('#price')\n+ await page.click('[data-testid=price]')\n"
	repairLinkFixture   = "https://solidping.example/d/orgs/acme/checks/c1/history"
)

// TestAppliedRepairMailEnglishIsUnchanged: the English notice reads as it did
// before it was localized.
func TestAppliedRepairMailEnglishIsUnchanged(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	subject, text := appliedRepairMail(i18n.English, "Pricing page", 4,
		repairReasonFixture, repairDiffFixture, repairLinkFixture)

	r.Equal("Check repaired: Pricing page", subject)
	r.Equal(fmt.Sprintf("SolidPing repaired the script of the check %q (version %d).\n\n%s\n\n"+
		"Diff:\n%s\nReview or undo it: %s\n", "Pricing page", 4,
		repairReasonFixture, repairDiffFixture, repairLinkFixture), text)
}

// TestAppliedRepairMailIsWrittenInTheOwnersLanguage: our words are
// translated, the reason and the diff are quoted as they are.
func TestAppliedRepairMailIsWrittenInTheOwnersLanguage(t *testing.T) {
	t.Parallel()

	expected := map[string][]string{
		"fr": {
			"Vérification réparée : Pricing page",
			"SolidPing a réparé le script de la vérification « Pricing page » (version 4).",
			"\nModifications :\n",
			"Pour relire ou annuler cette réparation : " + repairLinkFixture + "\n",
		},
		"de": {
			"Prüfung repariert: Pricing page",
			"SolidPing hat das Skript der Prüfung „Pricing page“ repariert (Version 4).",
			"\nÄnderungen:\n",
			"Reparatur prüfen oder rückgängig machen: " + repairLinkFixture + "\n",
		},
		"es": {
			"Comprobación reparada: Pricing page",
			"SolidPing reparó el script de la comprobación «Pricing page» (versión 4).",
			"\nCambios:\n",
			"Revisa o deshaz la reparación: " + repairLinkFixture + "\n",
		},
	}

	for lang, wants := range expected {
		r := require.New(t)

		subject, text := appliedRepairMail(i18n.New(lang, ""), "Pricing page", 4,
			repairReasonFixture, repairDiffFixture, repairLinkFixture)
		mail := subject + "\n" + text

		r.Contains(text, "\n\n"+repairReasonFixture+"\n\n", lang)
		r.Contains(text, repairDiffFixture, lang)
		r.NotContains(mail, "notices.", lang)

		for _, want := range wants {
			r.Contains(mail, want, lang)
		}
	}
}

// TestJobsMailerCarriesTheReadersLocale: the email job carries the locale
// the notice was written in, like every other email job.
func TestJobsMailerCarriesTheReadersLocale(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	mailer := &JobsMailer{Jobs: jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)}
	r.NoError(mailer.Mail(ctx, org.UID, []string{"alice@acme.com"}, i18n.New("fr", "Europe/Paris"),
		"Vérification réparée : x", "text"))

	var jobs []*models.Job

	r.NoError(dbSvc.DB().NewSelect().Model(&jobs).Where("type = ?", "email").Scan(ctx))
	r.Len(jobs, 1)

	raw, err := json.Marshal(jobs[0].Config)
	r.NoError(err)

	var payload map[string]any

	r.NoError(json.Unmarshal(raw, &payload))
	r.Equal("fr", payload["locale"])
	r.Equal("Europe/Paris", payload["timezone"])
	r.Equal("Vérification réparée : x", payload["subject"])
	r.Equal("text", payload["text"])
}
