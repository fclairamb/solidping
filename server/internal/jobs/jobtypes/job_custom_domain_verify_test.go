package jobtypes

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/domainverify"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

func newCustomDomainVerifyContext(t *testing.T) (*jobdef.JobContext, db.Service) {
	t.Helper()

	ctx := t.Context()
	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	jobSvc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

	jctx := &jobdef.JobContext{
		Services:  &services.Registry{Jobs: jobSvc},
		DB:        dbSvc.DB(),
		DBService: dbSvc,
		Logger:    slog.Default(),
	}

	return jctx, dbSvc
}

func seedCustomDomainPage(
	t *testing.T, dbSvc db.Service, domain string, verified *time.Time, failures int,
) *models.StatusPage {
	t.Helper()
	ctx := t.Context()

	org := models.NewOrganization("org-"+domain, "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	page := models.NewStatusPage(org.UID, "Acme", "main")
	token := "tok-" + domain
	page.CustomDomain = &domain
	page.CustomDomainToken = &token
	page.CustomDomainVerifiedAt = verified
	page.CustomDomainFailures = failures
	require.NoError(t, dbSvc.CreateStatusPage(ctx, page))

	return page
}

// stubVerifier returns a verifier whose TXT lookup passes iff pass is true.
func stubVerifier(pass bool, domain string) *domainverify.Verifier {
	return &domainverify.Verifier{
		LookupTXT: func(_ context.Context, _ string) ([]string, error) {
			if pass {
				return []string{domainverify.TXTValuePrefix + "tok-" + domain}, nil
			}

			return []string{"sp-domain-verify=wrong"}, nil
		},
		LookupCNAME: func(_ context.Context, _ string) (string, error) { return "", nil },
	}
}

func TestCustomDomainVerifyJob_Success(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	jctx, dbSvc := newCustomDomainVerifyContext(t)
	verified := time.Now().Add(-time.Hour)
	page := seedCustomDomainPage(t, dbSvc, "ok.acme.com", &verified, 2)

	run := &CustomDomainVerifyJobRun{verifier: stubVerifier(true, "ok.acme.com")}
	r.NoError(run.Run(ctx, jctx))

	updated, err := dbSvc.GetStatusPage(ctx, page.OrganizationUID, page.UID)
	r.NoError(err)
	r.Equal(0, updated.CustomDomainFailures, "success resets failures")
	r.NotNil(updated.CustomDomainVerifiedAt, "success keeps verification")
	r.NotNil(updated.CustomDomainCheckedAt, "checked_at stamped")
}

func TestCustomDomainVerifyJob_FailureIncrements(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	jctx, dbSvc := newCustomDomainVerifyContext(t)
	verified := time.Now().Add(-time.Hour)
	page := seedCustomDomainPage(t, dbSvc, "bad.acme.com", &verified, 0)

	run := &CustomDomainVerifyJobRun{verifier: stubVerifier(false, "bad.acme.com")}
	r.NoError(run.Run(ctx, jctx))

	updated, err := dbSvc.GetStatusPage(ctx, page.OrganizationUID, page.UID)
	r.NoError(err)
	r.Equal(1, updated.CustomDomainFailures, "failure increments count")
	r.NotNil(updated.CustomDomainVerifiedAt, "still verified below the threshold")
}

func TestCustomDomainVerifyJob_UnverifyAtThreshold(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	jctx, dbSvc := newCustomDomainVerifyContext(t)
	verified := time.Now().Add(-time.Hour)
	// Already at 2 failures; the 3rd clears verification.
	page := seedCustomDomainPage(t, dbSvc, "gone.acme.com", &verified, 2)

	run := &CustomDomainVerifyJobRun{verifier: stubVerifier(false, "gone.acme.com")}
	r.NoError(run.Run(ctx, jctx))

	updated, err := dbSvc.GetStatusPage(ctx, page.OrganizationUID, page.UID)
	r.NoError(err)
	r.Equal(3, updated.CustomDomainFailures)
	r.Nil(updated.CustomDomainVerifiedAt, "verification cleared at 3 failures")
}

func TestCustomDomainVerifyJob_Reschedules(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	jctx, _ := newCustomDomainVerifyContext(t)

	run := &CustomDomainVerifyJobRun{verifier: stubVerifier(true, "none")}
	r.NoError(run.Run(ctx, jctx))

	count, err := jctx.DB.NewSelect().
		Model((*models.Job)(nil)).
		Where("type = ?", string(jobdef.JobTypeCustomDomainVerify)).
		Where("status = ?", string(models.JobStatusPending)).
		Count(ctx)
	r.NoError(err)
	r.Positive(count, "job reschedules itself")
}
