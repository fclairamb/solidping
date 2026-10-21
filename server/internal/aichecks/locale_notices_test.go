package aichecks

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

const (
	repairDiffFixture = "- await page.click('#price')\n+ await page.click('[data-testid=price]')\n"
	repairLinkFixture = "https://solidping.example/d/orgs/acme/checks/c1/history"
	repairErrFixture  = "selector #price not found"
)

// failedRun is the last failed run a repair starts from.
func failedRun(output map[string]any) *models.Result {
	return &models.Result{Output: models.JSONMap(output)}
}

// legacyRepairReason is repairReason as it was before the notices were
// localized: the stored reasons must keep reading exactly like this.
func legacyRepairReason(latest *models.Result) string {
	msg := "the script drifted"

	if latest != nil {
		if errMsg, ok := latest.Output["error"].(string); ok && errMsg != "" {
			msg += ": " + errMsg
		} else if step, ok := latest.Output["step"].(string); ok && step != "" {
			msg += " at step " + step
		}
	}

	if len(msg) > 200 {
		msg = msg[:200] + "..."
	}

	return "AI repair: " + msg
}

// TestRepairReasonIsUnchanged: the reason stored on a version (and shown in
// the check history) is the English one, byte for byte, clipping included.
func TestRepairReasonIsUnchanged(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("é", 150)
	cases := map[string]*models.Result{
		"no run":     nil,
		"no detail":  failedRun(map[string]any{"failure": "drift"}),
		"error":      failedRun(map[string]any{"error": repairErrFixture, "step": "login"}),
		"step":       failedRun(map[string]any{"step": "login"}),
		"empty":      failedRun(map[string]any{"error": "", "step": ""}),
		"long error": failedRun(map[string]any{"error": long}),
		"long step":  failedRun(map[string]any{"step": long}),
		"braces":     failedRun(map[string]any{"error": "missing {step} in {error}"}),
		"edge error": failedRun(map[string]any{"error": strings.Repeat("x", 180)}),
		"edge+1":     failedRun(map[string]any{"error": strings.Repeat("x", 181)}),
		"edge step":  failedRun(map[string]any{"step": strings.Repeat("x", 174)}),
	}

	for name, latest := range cases {
		require.Equal(t, legacyRepairReason(latest), repairReason(latest), name)
	}
}

// TestAppliedRepairMailEnglishIsUnchanged: the English notice reads as it did
// before it was localized, the reason being the stored one.
func TestAppliedRepairMailEnglishIsUnchanged(t *testing.T) {
	t.Parallel()

	for _, latest := range []*models.Result{
		failedRun(map[string]any{"error": repairErrFixture}),
		failedRun(map[string]any{"step": "login"}),
		nil,
	} {
		r := require.New(t)

		subject, text := appliedRepairMail(i18n.English, "Pricing page", 4,
			causeOf(latest), repairDiffFixture, repairLinkFixture)

		r.Equal("Check repaired: Pricing page", subject)
		r.Equal(fmt.Sprintf("SolidPing repaired the script of the check %q (version %d).\n\n%s\n\n"+
			"Diff:\n%s\nReview or undo it: %s\n", "Pricing page", 4,
			legacyRepairReason(latest), repairDiffFixture, repairLinkFixture), text)
	}
}

// TestAppliedRepairMailIsWrittenInTheOwnersLanguage: every sentence we wrote
// is translated, the reason included. Only the failed run's error or step and
// the diff are quoted as the script and the model wrote them.
func TestAppliedRepairMailIsWrittenInTheOwnersLanguage(t *testing.T) {
	t.Parallel()

	type want struct {
		lines                 []string
		errReason, stepReason string
		driftedReason         string
	}

	expected := map[string]want{
		"fr": {
			lines: []string{
				"Contrôle réparé : Pricing page",
				"SolidPing a réparé le script du contrôle « Pricing page » (version 4).",
				"\nModifications :\n",
				"Pour relire ou annuler cette réparation : " + repairLinkFixture + "\n",
			},
			errReason:     "Réparation par IA : le script a dérivé (erreur : " + repairErrFixture + ")",
			stepReason:    "Réparation par IA : le script a dérivé à l'étape login",
			driftedReason: "Réparation par IA : le script a dérivé",
		},
		"de": {
			lines: []string{
				"Prüfung repariert: Pricing page",
				"SolidPing hat das Skript der Prüfung „Pricing page“ repariert (Version 4).",
				"\nÄnderungen:\n",
				"Reparatur ansehen oder rückgängig machen: " + repairLinkFixture + "\n",
			},
			errReason:     "KI-Reparatur: Das Skript ist abgewichen (Fehler: " + repairErrFixture + ")",
			stepReason:    "KI-Reparatur: Das Skript ist im Schritt login abgewichen",
			driftedReason: "KI-Reparatur: Das Skript ist abgewichen",
		},
		"es": {
			lines: []string{
				"Comprobación reparada: Pricing page",
				"SolidPing reparó el script de la comprobación «Pricing page» (versión 4).",
				"\nCambios:\n",
				"Revisa o deshaz la reparación: " + repairLinkFixture + "\n",
			},
			errReason:     "Reparación por IA: el script derivó (error: " + repairErrFixture + ")",
			stepReason:    "Reparación por IA: el script derivó en el paso login",
			driftedReason: "Reparación por IA: el script derivó",
		},
	}

	for lang, want := range expected {
		r := require.New(t)
		loc := i18n.New(lang, "")

		reasons := map[string]*models.Result{
			want.errReason:     failedRun(map[string]any{"error": repairErrFixture, "step": "login"}),
			want.stepReason:    failedRun(map[string]any{"step": "login"}),
			want.driftedReason: nil,
		}

		for reason, latest := range reasons {
			subject, text := appliedRepairMail(loc, "Pricing page", 4,
				causeOf(latest), repairDiffFixture, repairLinkFixture)
			mail := subject + "\n" + text

			r.Contains(text, "\n\n"+reason+"\n\n", lang)
			r.Contains(text, repairDiffFixture, lang)
			r.NotContains(mail, "notices.", lang)
			r.NotContains(mail, "AI repair", lang)
			r.NotContains(mail, "drifted", lang)
			r.NotContains(mail, "—", lang)

			for _, line := range want.lines {
				r.Contains(mail, line, lang)
			}
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
		"Contrôle réparé : x", "text"))

	var jobs []*models.Job

	r.NoError(dbSvc.DB().NewSelect().Model(&jobs).Where("type = ?", "email").Scan(ctx))
	r.Len(jobs, 1)

	raw, err := json.Marshal(jobs[0].Config)
	r.NoError(err)

	var payload map[string]any

	r.NoError(json.Unmarshal(raw, &payload))
	r.Equal("fr", payload["locale"])
	r.Equal("Europe/Paris", payload["timezone"])
	r.Equal("Contrôle réparé : x", payload["subject"])
	r.Equal("text", payload["text"])
}
