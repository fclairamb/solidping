package jobtypes

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/customdomain"
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

	state := models.CustomDomainStateActive
	if verified == nil {
		state = models.CustomDomainStatePending
	}

	return seedCustomDomainPageInState(t, dbSvc, domain, verified, failures, state)
}

// seedCustomDomainPageInState seeds a page in an explicit lifecycle state, for
// the transitions that only exist away from "freshly verified".
func seedCustomDomainPageInState(
	t *testing.T, dbSvc db.Service, domain string, verified *time.Time, failures int, state string,
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
	page.CustomDomainState = state
	require.NoError(t, dbSvc.CreateStatusPage(ctx, page))

	// A page that has been swept before must look like it: Normalize keys the
	// legacy fallback off custom_domain_checked_at.
	checked := time.Now().Add(-time.Hour)
	require.NoError(t, dbSvc.UpdateStatusPageCustomDomain(ctx, page.UID,
		&models.StatusPageCustomDomainUpdate{
			Domain:     page.CustomDomain,
			Token:      page.CustomDomainToken,
			VerifiedAt: verified,
			CheckedAt:  &checked,
			Failures:   failures,
			State:      state,
		}))

	reloaded, err := dbSvc.GetStatusPage(ctx, org.UID, page.UID)
	require.NoError(t, err)

	return reloaded
}

// seedStoredCertificate puts usable certificate material for host into
// tls_storage, so the sweep's "do we still hold a certificate" gate has
// something real to find.
func seedStoredCertificate(t *testing.T, dbSvc db.Service, host string, notAfter time.Time) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	require.NoError(t, err)

	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)

	base := "certificates/test-issuer/" + host + "/" + host
	ctx := t.Context()
	require.NoError(t, dbSvc.TLSStorageStore(ctx, base+".crt",
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
	require.NoError(t, dbSvc.TLSStorageStore(ctx, base+".key",
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})))
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

// TestCustomDomainVerifyJob_GraceKeepsServing is the regression guard for the
// defect this replaced: three consecutive failures used to clear the
// verification and take a customer's status page dark for what was, in the
// overwhelming majority of cases, a DNS blip. Now it enters `grace` and the
// page KEEPS SERVING.
func TestCustomDomainVerifyJob_GraceKeepsServing(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	jctx, dbSvc := newCustomDomainVerifyContext(t)
	verified := time.Now().Add(-time.Hour)
	page := seedCustomDomainPage(t, dbSvc, "blip.acme.com", &verified, customdomain.GraceAfterFailures-1)

	run := &CustomDomainVerifyJobRun{verifier: stubVerifier(false, "blip.acme.com")}
	r.NoError(run.Run(ctx, jctx))

	updated, err := dbSvc.GetStatusPage(ctx, page.OrganizationUID, page.UID)
	r.NoError(err)
	r.Equal(customdomain.GraceAfterFailures, updated.CustomDomainFailures)
	r.Equal(models.CustomDomainStateGrace, updated.CustomDomainState)
	r.NotNil(updated.CustomDomainVerifiedAt, "grace must keep the page serving")
	r.NotNil(updated.CustomDomainGraceSince)
}

// TestCustomDomainVerifyJob_HardDemotionAtTheFarThreshold covers the other end:
// a domain that really has gone away is eventually taken dark, and that is the
// event an operator gets told about.
func TestCustomDomainVerifyJob_HardDemotionAtTheFarThreshold(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	jctx, dbSvc := newCustomDomainVerifyContext(t)
	verified := time.Now().Add(-72 * time.Hour)
	page := seedCustomDomainPageInState(t, dbSvc, "gone.acme.com", &verified,
		customdomain.HardDemoteAfterFailures-1, models.CustomDomainStateGrace)

	run := &CustomDomainVerifyJobRun{verifier: stubVerifier(false, "gone.acme.com")}
	r.NoError(run.Run(ctx, jctx))

	updated, err := dbSvc.GetStatusPage(ctx, page.OrganizationUID, page.UID)
	r.NoError(err)
	r.Equal(customdomain.HardDemoteAfterFailures, updated.CustomDomainFailures)
	r.Equal(models.CustomDomainStateDemoted, updated.CustomDomainState)
	r.Nil(updated.CustomDomainVerifiedAt, "verification cleared at the hard-demotion threshold")
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
// dangling record). Sustained failure must still clear the verification — the
// grace window delays that, it does not remove it.
func TestCustomDomainVerifyJob_TokenModeDemotesSharedTargetCNAME(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	jctx, dbSvc := newCustomDomainVerifyContext(t)
	jctx.AppConfig.Server.CustomDomainCNAMEMode = string(domainverify.ModeToken)

	verified := time.Now().Add(-time.Hour)
	page := seedCustomDomainPageInState(t, dbSvc, "taken.acme.com", &verified,
		customdomain.HardDemoteAfterFailures-1, models.CustomDomainStateGrace)

	// The CNAME answers the SHARED target — valid in shared mode, invalid here.
	run := &CustomDomainVerifyJobRun{verifier: stubVerifier(true, "taken.acme.com")}
	r.NoError(run.Run(ctx, jctx))

	updated, err := dbSvc.GetStatusPage(ctx, page.OrganizationUID, page.UID)
	r.NoError(err)
	r.Equal(customdomain.HardDemoteAfterFailures, updated.CustomDomainFailures)
	r.Nil(updated.CustomDomainVerifiedAt, "shared target must not keep a token-mode page verified")
}

// TestCustomDomainVerifyJob_NeverPromotes locks the boundary of automatic
// re-promotion: a page that has NEVER verified stays unverified until an
// operator clicks Verify, however well it resolves. Only a domain that was
// ours before (state `demoted`) may earn its way back on its own.
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

// TestCustomDomainVerifyJob_RepromotesADemotedDomain is requirement 1 of spec
// 2026-08-23-03: a domain that was demoted and has started resolving to us
// again comes back on its own, with no human clicking Verify.
//
// It is deliberately driven through THREE full sweeps, not one, because a
// single success must not be enough — an implementation that re-promoted on the
// first pass would pass a one-sweep version of this test.
func TestCustomDomainVerifyJob_RepromotesADemotedDomain(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	jctx, dbSvc := newCustomDomainVerifyContext(t)
	page := seedCustomDomainPageInState(t, dbSvc, "back.acme.com", nil,
		customdomain.HardDemoteAfterFailures, models.CustomDomainStateDemoted)
	seedStoredCertificate(t, dbSvc, "back.acme.com", time.Now().Add(30*24*time.Hour))

	run := &CustomDomainVerifyJobRun{verifier: stubVerifier(true, "back.acme.com")}

	for sweep := 1; sweep < customdomain.RepromoteSuccesses; sweep++ {
		r.NoError(run.Run(ctx, jctx))

		partial, err := dbSvc.GetStatusPage(ctx, page.OrganizationUID, page.UID)
		r.NoError(err)
		r.Nil(partial.CustomDomainVerifiedAt, "sweep %d must not be enough on its own", sweep)
		r.Equal(sweep, partial.CustomDomainSuccesses)
	}

	r.NoError(run.Run(ctx, jctx))

	updated, err := dbSvc.GetStatusPage(ctx, page.OrganizationUID, page.UID)
	r.NoError(err)
	r.NotNil(updated.CustomDomainVerifiedAt, "the sweep must re-promote a recovered domain")
	r.Equal(models.CustomDomainStateActive, updated.CustomDomainState)
	r.Zero(updated.CustomDomainFailures)
}

// TestCustomDomainVerifyJob_NoRepromotionWithoutACertificate is the negative
// control for the test above: with the certificate gone from tls_storage, the
// same three successful sweeps must NOT bring the domain back. Without this,
// the re-promotion test would stay green if the certificate condition were
// deleted outright.
func TestCustomDomainVerifyJob_NoRepromotionWithoutACertificate(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	jctx, dbSvc := newCustomDomainVerifyContext(t)
	page := seedCustomDomainPageInState(t, dbSvc, "nocert.acme.com", nil,
		customdomain.HardDemoteAfterFailures, models.CustomDomainStateDemoted)

	run := &CustomDomainVerifyJobRun{verifier: stubVerifier(true, "nocert.acme.com")}
	for range customdomain.RepromoteSuccesses + 2 {
		r.NoError(run.Run(ctx, jctx))
	}

	updated, err := dbSvc.GetStatusPage(ctx, page.OrganizationUID, page.UID)
	r.NoError(err)
	r.Nil(updated.CustomDomainVerifiedAt, "no held certificate ⇒ no automatic re-promotion")
	r.Equal(models.CustomDomainStateDemoted, updated.CustomDomainState)
}

// TestCustomDomainVerifyJob_NoRepromotionWithAnExpiredCertificate is the second
// half of the same control: present-but-expired material must not count. An
// expired certificate cannot complete a handshake, so re-promoting on it would
// put the page back in exactly the failure mode this spec exists to remove.
func TestCustomDomainVerifyJob_NoRepromotionWithAnExpiredCertificate(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	jctx, dbSvc := newCustomDomainVerifyContext(t)
	page := seedCustomDomainPageInState(t, dbSvc, "expired.acme.com", nil,
		customdomain.HardDemoteAfterFailures, models.CustomDomainStateDemoted)
	seedStoredCertificate(t, dbSvc, "expired.acme.com", time.Now().Add(-time.Minute))

	run := &CustomDomainVerifyJobRun{verifier: stubVerifier(true, "expired.acme.com")}
	for range customdomain.RepromoteSuccesses + 2 {
		r.NoError(run.Run(ctx, jctx))
	}

	updated, err := dbSvc.GetStatusPage(ctx, page.OrganizationUID, page.UID)
	r.NoError(err)
	r.Nil(updated.CustomDomainVerifiedAt, "an expired certificate is not a certificate")
}

// TestCustomDomainVerifyJob_HardDemotionAlertsTheOrg is requirement 4: going
// dark must reach a human. The audit event lands in the org's activity feed and
// its webhook fan-out; the email job is the delivery that actually arrives.
func TestCustomDomainVerifyJob_HardDemotionAlertsTheOrg(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	jctx, dbSvc := newCustomDomainVerifyContext(t)
	verified := time.Now().Add(-72 * time.Hour)
	page := seedCustomDomainPageInState(t, dbSvc, "dark.acme.com", &verified,
		customdomain.HardDemoteAfterFailures-1, models.CustomDomainStateGrace)
	seedOrgAdmin(t, dbSvc, page.OrganizationUID, "admin@acme.com")

	run := &CustomDomainVerifyJobRun{verifier: stubVerifier(false, "dark.acme.com")}
	r.NoError(run.Run(ctx, jctx))

	events, err := dbSvc.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: page.OrganizationUID,
		EventTypes:      []models.EventType{models.EventTypeStatusPageCustomDomainDemoted},
		Limit:           10,
	})
	r.NoError(err)
	r.Len(events, 1, "hard demotion must record exactly one audit event")
	r.Equal("dark.acme.com", events[0].Payload["domain"])

	r.Equal(1, countEmailJobs(ctx, t, jctx), "the org's admin must be emailed")
}

// TestCustomDomainVerifyJob_GraceDoesNotAlert is the negative control for the
// alert: the state that keeps serving must stay quiet, or operators learn to
// ignore the message that means the page is actually down.
func TestCustomDomainVerifyJob_GraceDoesNotAlert(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	jctx, dbSvc := newCustomDomainVerifyContext(t)
	verified := time.Now().Add(-time.Hour)
	page := seedCustomDomainPage(t, dbSvc, "quiet.acme.com", &verified, customdomain.GraceAfterFailures-1)
	seedOrgAdmin(t, dbSvc, page.OrganizationUID, "admin@acme.com")

	run := &CustomDomainVerifyJobRun{verifier: stubVerifier(false, "quiet.acme.com")}
	r.NoError(run.Run(ctx, jctx))

	events, err := dbSvc.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: page.OrganizationUID,
		EventTypes:      []models.EventType{models.EventTypeStatusPageCustomDomainDemoted},
		Limit:           10,
	})
	r.NoError(err)
	r.Empty(events, "entering grace is not a demotion")
	r.Zero(countEmailJobs(ctx, t, jctx), "entering grace must not page anyone")
}

// TestCustomDomainVerifyJob_RecordsADiagnostic covers the diagnosability
// directive: a failed check must say what it saw, so "verification fails but
// dig says the record is right" is answerable from the row rather than by
// correlating pod logs with manual lookups.
func TestCustomDomainVerifyJob_RecordsADiagnostic(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	jctx, dbSvc := newCustomDomainVerifyContext(t)
	verified := time.Now().Add(-time.Hour)
	page := seedCustomDomainPage(t, dbSvc, "diag.acme.com", &verified, 0)

	run := &CustomDomainVerifyJobRun{verifier: stubVerifier(false, "diag.acme.com")}
	r.NoError(run.Run(ctx, jctx))

	updated, err := dbSvc.GetStatusPage(ctx, page.OrganizationUID, page.UID)
	r.NoError(err)
	r.NotNil(updated.CustomDomainLastCheck)
	r.Contains(*updated.CustomDomainLastCheck, "mode=shared", "the mode used must be recorded")
	r.Contains(*updated.CustomDomainLastCheck, "expected=cname.solidping.io")
	r.Contains(*updated.CustomDomainLastCheck, "resolved=elsewhere.example.net")
	r.Contains(*updated.CustomDomainLastCheck, "ok=false")
}

// seedOrgAdmin creates a user and makes them an admin of org, so the demotion
// alert has somewhere to go.
func seedOrgAdmin(t *testing.T, dbSvc db.Service, orgUID, email string) {
	t.Helper()
	ctx := t.Context()

	user := models.NewUser(email)
	require.NoError(t, dbSvc.CreateUser(ctx, user))
	require.NoError(t, dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(orgUID, user.UID, models.MemberRoleAdmin)))
}

// countEmailJobs counts queued email jobs, which is how the demotion alert is
// delivered.
func countEmailJobs(ctx context.Context, t *testing.T, jctx *jobdef.JobContext) int {
	t.Helper()

	count, err := jctx.DB.NewSelect().
		Model((*models.Job)(nil)).
		Where("type = ?", string(jobdef.JobTypeEmail)).
		Count(ctx)
	require.NoError(t, err)

	return count
}
