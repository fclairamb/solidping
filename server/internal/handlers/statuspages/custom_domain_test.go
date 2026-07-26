package statuspages

import (
	"context"
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
	r.Len(resp.CustomDomainRecords, 2)
	r.Equal("CNAME", resp.CustomDomainRecords[0].Type)
	r.Equal("cname.solidping.io", resp.CustomDomainRecords[0].Value)
	r.Equal("TXT", resp.CustomDomainRecords[1].Type)
	r.Contains(resp.CustomDomainRecords[1].Value, domainverify.TXTValuePrefix)
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

	// Inject a stub verifier that passes both checks.
	svc.verifier = &domainverify.Verifier{
		LookupTXT: func(_ context.Context, _ string) ([]string, error) {
			return []string{domainverify.TXTValuePrefix + token}, nil
		},
		LookupCNAME: func(_ context.Context, _ string) (string, error) {
			return "cname.solidping.io.", nil
		},
	}

	resp, err := svc.VerifyCustomDomain(ctx, org.Slug, page.UID)
	r.NoError(err)
	r.Equal("verified", resp.CustomDomainStatus)

	r.True(svc.CustomDomainServable(ctx, "status.acme.com"))
}

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
	svc.verifier = &domainverify.Verifier{
		LookupTXT: func(_ context.Context, _ string) ([]string, error) {
			return []string{domainverify.TXTValuePrefix + *stored.CustomDomainToken}, nil
		},
		LookupCNAME: func(_ context.Context, _ string) (string, error) { return "cname.solidping.io", nil },
	}
	_, err = svc.VerifyCustomDomain(ctx, org.Slug, page.UID)
	r.NoError(err)
	r.True(svc.CustomDomainServable(ctx, "status.acme.com"))
}
