package customdomain_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/customdomain"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// capturingJobs records the email jobs AlertDemoted queues.
type capturingJobs struct {
	jobsvc.Service
	configs []json.RawMessage
}

func (c *capturingJobs) CreateJob(
	_ context.Context, _, _ string, config json.RawMessage, _ *jobsvc.JobOptions,
) (*models.Job, error) {
	c.configs = append(c.configs, config)

	return &models.Job{UID: "job"}, nil
}

type demotedJob struct {
	To           []string       `json:"to"`
	Template     string         `json:"template"`
	TemplateData map[string]any `json:"templateData"`
	Locale       string         `json:"locale"`
	Timezone     string         `json:"timezone"`
}

func addAdmin(t *testing.T, dbSvc *sqlite.Service, orgUID, address string, prefs *models.UserPreferences) {
	t.Helper()

	user := models.NewUser(address)
	user.Preferences = prefs
	require.NoError(t, dbSvc.CreateUser(t.Context(), user))
	require.NoError(t, dbSvc.CreateOrganizationMember(t.Context(),
		models.NewOrganizationMember(orgUID, user.UID, models.MemberRoleAdmin)))
}

// TestAlertDemoted_SpeaksEachAdminsLanguage: one job per admin, in that
// admin's language and zone; the diagnostic stays the verifier's technical
// line in every language.
func TestAlertDemoted_SpeaksEachAdminsLanguage(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(t.Context()))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(t.Context(), org))
	addAdmin(t, dbSvc, org.UID, "lea@acme.com", &models.UserPreferences{Locale: "fr", Timezone: "Europe/Paris"})
	addAdmin(t, dbSvc, org.UID, "sam@acme.com", nil)

	page := models.NewStatusPage(org.UID, "Acme Status", "status")
	domain := "status.acme.com"
	page.CustomDomain = &domain

	jobs := &capturingJobs{}
	diagnostic := "mode=shared expected=status.solidping.example resolved=<none> ok=false error=lookup failed"
	customdomain.AlertDemoted(t.Context(), customdomain.AlertDeps{DB: dbSvc, Jobs: jobs}, page, 11, diagnostic)
	r.Len(jobs.configs, 2)

	formatter, err := email.NewFormatter()
	r.NoError(err)

	byRecipient := map[string]demotedJob{}

	for _, raw := range jobs.configs {
		var job demotedJob
		r.NoError(json.Unmarshal(raw, &job))
		byRecipient[job.To[0]] = job
	}

	lea := byRecipient["lea@acme.com"]
	r.Equal("fr", lea.Locale)
	r.Equal("Europe/Paris", lea.Timezone)

	subject, _, text, err := formatter.Format(lea.Template, i18n.New(lea.Locale, lea.Timezone), lea.TemplateData)
	r.NoError(err)
	r.Equal("🟠 Votre page de statut n'est plus servie sur status.acme.com", subject)
	r.Contains(text, "La dernière vérification a renvoyé :\n\n  "+diagnostic)

	sam := byRecipient["sam@acme.com"]
	r.Equal(i18n.LangEnglish, sam.Locale, "an admin without preferences reads English, never the owner's")
	r.Equal("UTC", sam.Timezone)
}
