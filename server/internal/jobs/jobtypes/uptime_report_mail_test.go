package jobtypes

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// createReader adds an account with the given preferences ("" for none).
func createReader(t *testing.T, dbSvc *sqlite.Service, email, locale, timezone string) *models.User {
	t.Helper()

	user := models.NewUser(email)
	if locale != "" || timezone != "" {
		user.Preferences = &models.UserPreferences{Locale: locale, Timezone: timezone}
	}

	require.NoError(t, dbSvc.CreateUser(t.Context(), user))

	return user
}

// TestUptimeReportIsWrittenInEachRecipientsLanguage: every recipient's mail is
// written and rendered in their own language (an address with no account in
// the org owner's), its period stays the schedule's month, and each keeps its
// own unsubscribe link.
func TestUptimeReportIsWrittenInEachRecipientsLanguage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)

	dbSvc, org, jctx, jobs := newReportEnv(t, now)
	newCheck(t, dbSvc, org)

	owner := createReader(t, dbSvc, "owner@acme.com", "de", "Europe/Berlin")
	r.NoError(dbSvc.CreateOrganizationMember(ctx, &models.OrganizationMember{
		UID: uuid.New().String(), UserUID: owner.UID, OrganizationUID: org.UID, Role: models.MemberRoleOwner,
	}))
	createReader(t, dbSvc, "lea@acme.com", "fr", "America/New_York")
	createReader(t, dbSvc, "bare@acme.com", "", "")

	schedule := models.NewReportSchedule(org.UID, "Monthly digest", models.ReportFrequencyMonthly)
	schedule.Timezone = "Europe/Paris"
	schedule.Recipients = []string{"Lea@acme.com", "ops-list@acme.com", "bare@acme.com"}
	r.NoError(dbSvc.CreateReportSchedule(ctx, schedule))

	r.NoError((&UptimeReportJobRun{}).Run(ctx, jctx))

	type mail struct {
		locale, timezone, period, scope, unsubscribe string
	}

	got := map[string]mail{}

	for _, job := range jobs.created {
		if job.jobType != string(jobdef.JobTypeEmail) {
			continue
		}

		var cfg EmailJobConfig
		r.NoError(json.Unmarshal(job.config, &cfg))
		r.Len(cfg.To, 1)

		data, ok := cfg.TemplateData.(map[string]any)
		r.True(ok)
		r.Equal(cfg.ListUnsubscribeURL, data["UnsubscribeURL"])

		period, ok := data["PeriodLabel"].(string)
		r.True(ok)

		scope, ok := data["ScopeLabel"].(string)
		r.True(ok)

		got[cfg.To[0]] = mail{
			locale:      cfg.Locale,
			timezone:    cfg.Timezone,
			period:      period,
			scope:       scope,
			unsubscribe: cfg.ListUnsubscribeURL,
		}
	}

	r.Len(got, 3)

	// The account's language and zone; the period is still Paris's July
	// although July 1 00:00 Paris is June 30 in New York.
	r.Equal("fr", got["Lea@acme.com"].locale)
	r.Equal("America/New_York", got["Lea@acme.com"].timezone)
	r.Equal("juillet 2026", got["Lea@acme.com"].period)
	r.Equal("Tous les contrôles (1)", got["Lea@acme.com"].scope)

	// No account: the org owner's preferences.
	r.Equal("de", got["ops-list@acme.com"].locale)
	r.Equal("Europe/Berlin", got["ops-list@acme.com"].timezone)
	r.Equal("Juli 2026", got["ops-list@acme.com"].period)

	// An account without preferences reads English, never the owner's.
	r.Equal("en", got["bare@acme.com"].locale)
	r.Equal("UTC", got["bare@acme.com"].timezone)
	r.Equal("July 2026", got["bare@acme.com"].period)

	// One unsubscribe link per recipient, never shared.
	r.NotEqual(got["Lea@acme.com"].unsubscribe, got["ops-list@acme.com"].unsubscribe)
	r.NotEqual(got["Lea@acme.com"].unsubscribe, got["bare@acme.com"].unsubscribe)
}
