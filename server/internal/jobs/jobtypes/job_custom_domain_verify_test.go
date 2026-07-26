package jobtypes

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
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

	appCfg := &config.Config{}
	appCfg.Server.BaseURL = "https://solidping.io"
	appCfg.Server.CustomDomainCNAMETarget = "cname.solidping.io"

	jctx := &jobdef.JobContext{
		Services:  &services.Registry{Jobs: jobSvc},
		DB:        dbSvc.DB(),
		DBService: dbSvc,
		AppConfig: appCfg,
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
	token := customDomainTestToken
	page.CustomDomain = &domain
	page.CustomDomainToken = &token
	page.CustomDomainVerifiedAt = verified
	page.CustomDomainFailures = failures
	require.NoError(t, dbSvc.CreateStatusPage(ctx, page))

	return page
}

// customDomainTestToken is the per-page token seeded on every fixture page. It
// is DNS-label shaped so token-mode host construction succeeds.
const customDomainTestToken = "sptesttoken"

// stubVerifier returns a verifier whose CNAME lookup answers the shared target
// iff pass is true (and something else otherwise).
func stubVerifier(pass bool, _ string) *domainverify.Verifier {
	return &domainverify.Verifier{
		LookupCNAME: func(_ context.Context, _ string) (string, error) {
			if pass {
				return "cname.solidping.io.", nil
			}

			return "elsewhere.example.net.", nil
		},
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

// TestCustomDomainVerifyJob_TokenModeDemotesSharedTargetCNAME is the takeover
// scenario the re-verify sweep exists for: the installation runs token mode and
// a page's CNAME has been repointed at the plain shared target (or is a leftover
// dangling record). Three sweeps must clear the verification.
func TestCustomDomainVerifyJob_TokenModeDemotesSharedTargetCNAME(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	jctx, dbSvc := newCustomDomainVerifyContext(t)
	jctx.AppConfig.Server.CustomDomainCNAMEMode = string(domainverify.ModeToken)

	verified := time.Now().Add(-time.Hour)
	page := seedCustomDomainPage(t, dbSvc, "taken.acme.com", &verified, 2)

	// The CNAME answers the SHARED target — valid in shared mode, invalid here.
	run := &CustomDomainVerifyJobRun{verifier: stubVerifier(true, "taken.acme.com")}
	r.NoError(run.Run(ctx, jctx))

	updated, err := dbSvc.GetStatusPage(ctx, page.OrganizationUID, page.UID)
	r.NoError(err)
	r.Equal(3, updated.CustomDomainFailures)
	r.Nil(updated.CustomDomainVerifiedAt, "shared target must not keep a token-mode page verified")
}

// TestCustomDomainVerifyJob_NeverPromotes locks the rule that the sweep only
// ever demotes: an unverified page that now passes the CNAME check stays
// unverified until an operator clicks Verify.
func TestCustomDomainVerifyJob_NeverPromotes(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	jctx, dbSvc := newCustomDomainVerifyContext(t)
	page := seedCustomDomainPage(t, dbSvc, "fresh.acme.com", nil, 0)

	run := &CustomDomainVerifyJobRun{verifier: stubVerifier(true, "fresh.acme.com")}
	r.NoError(run.Run(ctx, jctx))

	updated, err := dbSvc.GetStatusPage(ctx, page.OrganizationUID, page.UID)
	r.NoError(err)
	r.Nil(updated.CustomDomainVerifiedAt, "the sweep never promotes")
	r.Equal(0, updated.CustomDomainFailures)
	r.NotNil(updated.CustomDomainCheckedAt)
}

// TestCustomDomainVerifyJob_NoConfigFailsClosed proves a job context with no
// app config cannot accidentally verify anything.
func TestCustomDomainVerifyJob_NoConfigFailsClosed(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	jctx, dbSvc := newCustomDomainVerifyContext(t)
	jctx.AppConfig = nil

	verified := time.Now().Add(-time.Hour)
	page := seedCustomDomainPage(t, dbSvc, "noconf.acme.com", &verified, 0)

	run := &CustomDomainVerifyJobRun{verifier: stubVerifier(true, "noconf.acme.com")}
	r.NoError(run.Run(ctx, jctx))

	updated, err := dbSvc.GetStatusPage(ctx, page.OrganizationUID, page.UID)
	r.NoError(err)
	r.Equal(1, updated.CustomDomainFailures, "no CNAME target configured → check fails")
}
