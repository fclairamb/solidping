package feedback

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
)

// TestSubmitReport_ResolveOrg covers the organization lookup: the named org,
// the first-org fallback, an unknown slug and an instance without any org.
func TestSubmitReport_ResolveOrg(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	empty, _ := newBudgetSQLite(t)
	svc, _ := newBudgetService(t, empty, 1024, 20)

	_, err := svc.SubmitReport(ctx, &SubmitReportRequest{URL: "https://example.com"})
	r.ErrorIs(err, ErrOrganizationNotFound, "no org at all")

	dbSvc, orgs := newBudgetSQLite(t, "acme-resolve")
	svc, _ = newBudgetService(t, dbSvc, 1024, 20)

	resp, err := svc.SubmitReport(ctx, screenshotReport("", ""))
	r.NoError(err, "an empty slug falls back to the first org")
	r.NotEmpty(resp.UID)
	r.Equal(int64(1), reportBytes(ctx, t, dbSvc, orgs[0]))

	_, err = svc.SubmitReport(ctx, &SubmitReportRequest{URL: "https://example.com", OrgSlug: "nope"})
	r.ErrorIs(err, ErrOrganizationNotFound, "unknown slug")
}

// TestSubmitReport_AnonScreenshotWithoutGitHub: with no GitHub configured no
// issue slot exists, so an anonymous screenshot is refused and nothing is
// stored, while a member's report and a text-only one are still accepted.
func TestSubmitReport_AnonScreenshotWithoutGitHub(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, orgs := newBudgetSQLite(t, "acme-nogh")
	cfg := newFeedbackQuotaConfig(t, 1024)
	cfg.App.GitHub = config.AppGitHubConfig{}

	svc := NewService(dbSvc, files.NewService(dbSvc, cfg), cfg, nil)
	member := newMember(t, dbSvc)

	_, err := svc.SubmitReport(ctx, screenshotReport(orgs[0].Slug, ""))
	r.ErrorIs(err, ErrReportRateLimited)
	r.Zero(reportBytes(ctx, t, dbSvc, orgs[0]))

	_, err = svc.SubmitReport(ctx, screenshotReport(orgs[0].Slug, member))
	r.NoError(err)

	_, err = svc.SubmitReport(ctx, &SubmitReportRequest{URL: "https://example.com", OrgSlug: orgs[0].Slug})
	r.NoError(err)
}

// TestSubmitReport_AnonBudgetDisabled: a budget of 0 lifts the anonymous cap.
func TestSubmitReport_AnonBudgetDisabled(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, orgs := newBudgetSQLite(t, "acme-nobudget")
	svc, _ := newBudgetService(t, dbSvc, 1024, 0)

	for range 3 {
		_, err := svc.SubmitReport(ctx, screenshotReport(orgs[0].Slug, ""))
		r.NoError(err)
	}

	r.Equal(int64(3), reportBytes(ctx, t, dbSvc, orgs[0]))
}

// assertListFilesByGroup checks the cross-org listing used by the retention
// sweep: group matching, the age cut-off, the limit and an empty group.
func assertListFilesByGroup(ctx context.Context, t *testing.T, dbSvc db.Service, orgA, orgB *models.Organization) {
	t.Helper()
	r := require.New(t)

	svc, _ := newBudgetService(t, dbSvc, 1024, 0)

	for _, org := range []*models.Organization{orgA, orgA, orgB} {
		_, err := svc.SubmitReport(ctx, screenshotReport(org.Slug, ""))
		r.NoError(err)
	}

	future := time.Now().Add(time.Hour)

	all, err := dbSvc.ListFilesByGroup(ctx, "reports", future, 0)
	r.NoError(err)
	r.GreaterOrEqual(len(all), 3, "files of every org are listed, with no limit")

	limited, err := dbSvc.ListFilesByGroup(ctx, "reports", future, 2)
	r.NoError(err)
	r.Len(limited, 2)

	none, err := dbSvc.ListFilesByGroup(ctx, "reports", time.Now().Add(-24*time.Hour), 0)
	r.NoError(err)
	r.Empty(none, "nothing is older than a day")

	none, err = dbSvc.ListFilesByGroup(ctx, "", future, 0)
	r.NoError(err)
	r.Empty(none, "an empty group matches nothing")
}

func TestListFilesByGroup_SQLite(t *testing.T) {
	t.Parallel()

	dbSvc, orgs := newBudgetSQLite(t, "lfg-a", "lfg-b")
	assertListFilesByGroup(t.Context(), t, dbSvc, orgs[0], orgs[1])
}

func TestListFilesByGroup_Postgres(t *testing.T) {
	t.Parallel()

	dbSvc, orgA := newFeedbackPostgresOrg(t, "fb-lfg-pg-a")
	_, orgB := newFeedbackPostgresOrg(t, "fb-lfg-pg-b")
	assertListFilesByGroup(t.Context(), t, dbSvc, orgA, orgB)
}
