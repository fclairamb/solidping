package statuspages

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/domainverify"
	"github.com/fclairamb/solidping/server/internal/entitlements"
)

func setupCustomDomainTest(t *testing.T) (context.Context, *Service, *models.Organization) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))
	t.Cleanup(func() { _ = dbService.Close() })

	org := models.NewOrganization("acme", "Acme")
	require.NoError(t, dbService.CreateOrganization(ctx, org))

	cfg := &config.Config{}
	cfg.Server.BaseURL = "https://solidping.io"
	cfg.Server.DocsHost = "docs.solidping.io"
	cfg.Server.CustomDomainCNAMETarget = "cname.solidping.io"

	return ctx, NewService(dbService, cfg, nil), org
}

func mkPage(t *testing.T, svc *Service, org *models.Organization, slug string) *models.StatusPage {
	t.Helper()
	page := models.NewStatusPage(org.UID, slug, slug)
	require.NoError(t, svc.db.CreateStatusPage(t.Context(), page))

	return page
}

func strptr(s string) *string { return &s }

func TestSetCustomDomain_NormalizesAndGeneratesRecords(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupCustomDomainTest(t)
	page := mkPage(t, svc, org, "main")

	resp, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr("Status.ACME.com"), CustomDomainSet: true,
	})
	r.NoError(err)
	r.NotNil(resp.CustomDomain)
	r.Equal("status.acme.com", *resp.CustomDomain, "domain is lowercased/normalized")
	r.Equal("unverified", resp.CustomDomainStatus)
	// v0.8.0 contract: exactly ONE record, and never a TXT challenge.
	r.Len(resp.CustomDomainRecords, 1)
	r.Equal("CNAME", resp.CustomDomainRecords[0].Type)
	r.Equal("status.acme.com", resp.CustomDomainRecords[0].Name)
	r.Equal("cname.solidping.io", resp.CustomDomainRecords[0].Value)
}

// TestSetCustomDomain_TokenModeRecordsPerPageTarget proves the token-mode UX is
// still a single CNAME, but pointed at the page-specific host.
func TestSetCustomDomain_TokenModeRecordsPerPageTarget(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupCustomDomainTest(t)
	svc.cfg.Server.CustomDomainCNAMEMode = string(domainverify.ModeToken)
	page := mkPage(t, svc, org, "main")

	resp, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr("status.acme.com"), CustomDomainSet: true,
	})
	r.NoError(err)
	r.Len(resp.CustomDomainRecords, 1)
	r.Equal("CNAME", resp.CustomDomainRecords[0].Type)

	stored, err := svc.db.GetStatusPage(ctx, org.UID, page.UID)
	r.NoError(err)
	r.Equal(*stored.CustomDomainToken+".cname.cname.solidping.io", resp.CustomDomainRecords[0].Value)
}

// TestGenerateCustomDomainToken_IsDNSLabelSafe locks the token shape: it has to
// be usable as the leading DNS label of the token-mode CNAME target.
func TestGenerateCustomDomainToken_IsDNSLabelSafe(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	const dnsLabelMax = 63

	for range 50 {
		token, err := generateCustomDomainToken()
		r.NoError(err)
		r.LessOrEqual(len(token), dnsLabelMax)
		r.NotEmpty(token)
		r.Regexp("^[a-z][a-z0-9]*$", token)
		// It must survive host construction (TokenHost fails closed otherwise).
		r.Equal(token+".cname.solidping.io", domainverify.TokenHost(token, "solidping.io"))
	}
}

func TestSetCustomDomain_Invalid(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupCustomDomainTest(t)
	page := mkPage(t, svc, org, "main")

	_, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr("localhost"), CustomDomainSet: true,
	})
	r.ErrorIs(err, ErrCustomDomainInvalid)
}

func TestSetCustomDomain_SelfShadow(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupCustomDomainTest(t)
	page := mkPage(t, svc, org, "main")

	// Equal to the CNAME target.
	_, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr("cname.solidping.io"), CustomDomainSet: true,
	})
	r.ErrorIs(err, ErrCustomDomainSelfShadow)

	// Subdomain of the base-url host.
	_, err = svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr("foo.solidping.io"), CustomDomainSet: true,
	})
	r.ErrorIs(err, ErrCustomDomainSelfShadow)
}

func TestSetCustomDomain_Conflict(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupCustomDomainTest(t)
	page1 := mkPage(t, svc, org, "one")
	page2 := mkPage(t, svc, org, "two")

	_, err := svc.UpdateStatusPage(ctx, org.Slug, page1.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr("status.acme.com"), CustomDomainSet: true,
	})
	r.NoError(err)

	_, err = svc.UpdateStatusPage(ctx, org.Slug, page2.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr("status.acme.com"), CustomDomainSet: true,
	})
	r.ErrorIs(err, ErrCustomDomainTaken)
}

func TestClearCustomDomain(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupCustomDomainTest(t)
	page := mkPage(t, svc, org, "main")

	_, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr("status.acme.com"), CustomDomainSet: true,
	})
	r.NoError(err)

	// Explicit empty clears.
	resp, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr(""), CustomDomainSet: true,
	})
	r.NoError(err)
	r.Nil(resp.CustomDomain)
	r.Empty(resp.CustomDomainRecords)
}

func TestUpdate_OmittedCustomDomainLeavesItUntouched(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupCustomDomainTest(t)
	page := mkPage(t, svc, org, "main")

	_, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr("status.acme.com"), CustomDomainSet: true,
	})
	r.NoError(err)

	// A later update that does not touch the domain (CustomDomainSet=false) must
	// keep it.
	resp, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		Name: strptr("Renamed"),
	})
	r.NoError(err)
	r.NotNil(resp.CustomDomain)
	r.Equal("status.acme.com", *resp.CustomDomain)
}

func TestSetCustomDomain_EntitlementDenied(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupCustomDomainTest(t)

	// Rebuild the service with an entitlements service capping MaxCustomDomains
	// at 1 (defaults apply to every org with no stored row).
	ent := entitlements.NewService(svc.db, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxCustomDomains: entitlements.Int(1)},
	}, 0)
	svc.ent = ent

	page1 := mkPage(t, svc, org, "one")
	page2 := mkPage(t, svc, org, "two")

	_, err := svc.UpdateStatusPage(ctx, org.Slug, page1.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr("a.example.com"), CustomDomainSet: true,
	})
	r.NoError(err)

	_, err = svc.UpdateStatusPage(ctx, org.Slug, page2.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr("b.example.com"), CustomDomainSet: true,
	})
	r.ErrorIs(err, entitlements.ErrEntitlementExceeded)
}

func TestVerifyCustomDomain(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupCustomDomainTest(t)
	page := mkPage(t, svc, org, "main")

	_, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr("status.acme.com"), CustomDomainSet: true,
	})
	r.NoError(err)

	stored, err := svc.db.GetStatusPage(ctx, org.UID, page.UID)
	r.NoError(err)
	token := *stored.CustomDomainToken

	r.NotEmpty(token)

	// Inject a stub verifier whose CNAME answer matches the shared target.
	svc.verifier = &domainverify.Verifier{
		LookupCNAME: func(_ context.Context, _ string) (string, error) {
			return "cname.solidping.io.", nil
		},
	}

	resp, err := svc.VerifyCustomDomain(ctx, org.Slug, page.UID)
	r.NoError(err)
	r.Equal("verified", resp.CustomDomainStatus)

	r.True(svc.CustomDomainServable(ctx, "status.acme.com"))
}

// TestVerifyCustomDomain_TokenModeRejectsSharedTarget is the no-dual-accept
// negative control: with token mode configured, a CNAME at the plain shared
// target must NOT verify — otherwise the dangling-CNAME protection is void.
func TestVerifyCustomDomain_TokenModeRejectsSharedTarget(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupCustomDomainTest(t)
	svc.cfg.Server.CustomDomainCNAMEMode = string(domainverify.ModeToken)
	page := mkPage(t, svc, org, "main")

	_, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr("status.acme.com"), CustomDomainSet: true,
	})
	r.NoError(err)

	stored, err := svc.db.GetStatusPage(ctx, org.UID, page.UID)
	r.NoError(err)

	svc.verifier = &domainverify.Verifier{
		LookupCNAME: func(_ context.Context, _ string) (string, error) {
			return "cname.solidping.io.", nil
		},
	}

	resp, err := svc.VerifyCustomDomain(ctx, org.Slug, page.UID)
	r.NoError(err)
	r.Equal("unverified", resp.CustomDomainStatus)
	r.False(svc.CustomDomainServable(ctx, "status.acme.com"))

	// The page's own token host does verify.
	svc.verifier = &domainverify.Verifier{
		LookupCNAME: func(_ context.Context, _ string) (string, error) {
			return *stored.CustomDomainToken + ".cname.cname.solidping.io.", nil
		},
	}

	resp, err = svc.VerifyCustomDomain(ctx, org.Slug, page.UID)
	r.NoError(err)
	r.Equal("verified", resp.CustomDomainStatus)
}

// TestCertStatusOnlyWhenVerifiedAndProviderPresent covers the
// customDomainCertStatus surfacing rules.
func TestCertStatusOnlyWhenVerifiedAndProviderPresent(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupCustomDomainTest(t)
	page := mkPage(t, svc, org, "main")

	resp, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr("status.acme.com"), CustomDomainSet: true,
	})
	r.NoError(err)
	r.Empty(resp.CustomDomainCertStatus, "no provider wired → field omitted")

	svc.SetCertStatusProvider(stubCertStatus("issued"))

	// Still unverified → no cert status (nothing can have been issued).
	resp, err = svc.GetStatusPage(ctx, org.Slug, page.UID, GetStatusPageOptions{})
	r.NoError(err)
	r.Empty(resp.CustomDomainCertStatus)

	svc.verifier = &domainverify.Verifier{
		LookupCNAME: func(_ context.Context, _ string) (string, error) { return "cname.solidping.io", nil },
	}
	verified, err := svc.VerifyCustomDomain(ctx, org.Slug, page.UID)
	r.NoError(err)
	r.Equal("issued", verified.CustomDomainCertStatus)
}

// stubCertStatus is a fixed-answer CertStatusProvider.
type stubCertStatus string

func (s stubCertStatus) CertStatus(string) string { return string(s) }

func TestPublicViewOmitsCustomDomain(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupCustomDomainTest(t)
	page := mkPage(t, svc, org, "public")

	_, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr("status.acme.com"), CustomDomainSet: true,
	})
	r.NoError(err)

	// Public view must never carry the custom domain or its records/token.
	pub, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug)
	r.NoError(err)
	r.Nil(pub.CustomDomain)
	r.Empty(pub.CustomDomainStatus)
	r.Empty(pub.CustomDomainRecords)
}

func TestCustomDomainServable_Gating(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupCustomDomainTest(t)
	page := mkPage(t, svc, org, "main")

	_, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr("status.acme.com"), CustomDomainSet: true,
	})
	r.NoError(err)

	// Unverified → not servable.
	r.False(svc.CustomDomainServable(ctx, "status.acme.com"))
	r.False(svc.CustomDomainServable(ctx, "unknown.example.com"))

	// Mark verified directly, then it is servable.
	stored, err := svc.db.GetStatusPage(ctx, org.UID, page.UID)
	r.NoError(err)
	r.NotNil(stored.CustomDomainToken)
	svc.verifier = &domainverify.Verifier{
		LookupCNAME: func(_ context.Context, _ string) (string, error) { return "cname.solidping.io", nil },
	}
	_, err = svc.VerifyCustomDomain(ctx, org.Slug, page.UID)
	r.NoError(err)
	r.True(svc.CustomDomainServable(ctx, "status.acme.com"))
}

// recordingEdge is a CertStatusProvider that also implements DomainForgetter,
// exactly as tlsedge.Edge does, so the optional interface assertion in
// SetCertStatusProvider is exercised rather than assumed.
type recordingEdge struct {
	mu       sync.Mutex
	forgoten []string
}

func (e *recordingEdge) CertStatus(string) string { return "issued" }

func (e *recordingEdge) ForgetDomain(_ context.Context, domain string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.forgoten = append(e.forgoten, domain)
}

func (e *recordingEdge) seen() []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	return append([]string(nil), e.forgoten...)
}

// TestClearingCustomDomainForgetsTLSMaterial pins the cleanup half of the
// takeover fix: clearing a domain must not leave its certificate and private
// key sitting in tls_storage for the rest of their 90-day life.
func TestClearingCustomDomainForgetsTLSMaterial(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupCustomDomainTest(t)
	page := mkPage(t, svc, org, "main")

	edge := &recordingEdge{}
	svc.SetCertStatusProvider(edge)

	_, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr("status.acme.com"), CustomDomainSet: true,
	})
	r.NoError(err)
	r.Empty(edge.seen(), "setting a domain must not forget anything")

	_, err = svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		CustomDomain: nil, CustomDomainSet: true,
	})
	r.NoError(err)
	r.Equal([]string{"status.acme.com"}, edge.seen(), "clearing must drop the domain's TLS material")
}

// TestChangingCustomDomainForgetsThePreviousOne covers the other way a
// hostname stops being ours: it is replaced rather than removed.
func TestChangingCustomDomainForgetsThePreviousOne(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupCustomDomainTest(t)
	page := mkPage(t, svc, org, "main")

	edge := &recordingEdge{}
	svc.SetCertStatusProvider(edge)

	_, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr("old.acme.com"), CustomDomainSet: true,
	})
	r.NoError(err)

	// Re-sending the SAME domain is a no-op and must not forget it.
	_, err = svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr("old.acme.com"), CustomDomainSet: true,
	})
	r.NoError(err)
	r.Empty(edge.seen(), "an unchanged domain must keep its certificate")

	_, err = svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr("new.acme.com"), CustomDomainSet: true,
	})
	r.NoError(err)
	r.Equal([]string{"old.acme.com"}, edge.seen(), "the replaced hostname must be forgotten")
}

// TestCertStatusProviderWithoutForgetterIsSafe pins that a provider which does
// NOT implement DomainForgetter still works — the assertion is optional.
func TestCertStatusProviderWithoutForgetterIsSafe(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupCustomDomainTest(t)
	page := mkPage(t, svc, org, "main")

	svc.SetCertStatusProvider(stubCertStatus("issued"))

	_, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		CustomDomain: strptr("status.acme.com"), CustomDomainSet: true,
	})
	r.NoError(err)

	_, err = svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		CustomDomain: nil, CustomDomainSet: true,
	})
	r.NoError(err, "clearing must not require a DomainForgetter")
}
