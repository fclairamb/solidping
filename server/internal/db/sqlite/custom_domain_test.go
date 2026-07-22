package sqlite

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func newCustomDomainTestService(t *testing.T) *Service {
	t.Helper()

	ctx := t.Context()
	s, err := New(ctx, Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	return s
}

func TestGetStatusPageByCustomDomain(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s := newCustomDomainTestService(t)

	org := models.NewOrganization("cd-org", "CD Org")
	r.NoError(s.CreateOrganization(ctx, org))

	page := models.NewStatusPage(org.UID, "Acme", "main")
	domain := "status.acme.com"
	token := "tok"
	page.CustomDomain = &domain
	page.CustomDomainToken = &token
	r.NoError(s.CreateStatusPage(ctx, page))

	got, err := s.GetStatusPageByCustomDomain(ctx, domain)
	r.NoError(err)
	r.NotNil(got)
	r.Equal(page.UID, got.UID)

	// Soft-deleted pages are excluded.
	r.NoError(s.DeleteStatusPage(ctx, page.UID))
	_, err = s.GetStatusPageByCustomDomain(ctx, domain)
	r.True(errors.Is(err, sql.ErrNoRows))
}

func TestUpdateStatusPageCustomDomain_SetAndClear(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s := newCustomDomainTestService(t)

	org := models.NewOrganization("cd-org2", "CD Org 2")
	r.NoError(s.CreateOrganization(ctx, org))

	page := models.NewStatusPage(org.UID, "Acme", "main")
	r.NoError(s.CreateStatusPage(ctx, page))

	domain := "status.acme.com"
	token := "tok"
	now := time.Now()
	r.NoError(s.UpdateStatusPageCustomDomain(ctx, page.UID, &models.StatusPageCustomDomainUpdate{
		Domain: &domain, Token: &token, VerifiedAt: &now, CheckedAt: &now, Failures: 0,
	}))

	got, err := s.GetStatusPage(ctx, org.UID, page.UID)
	r.NoError(err)
	r.Equal(domain, *got.CustomDomain)
	r.NotNil(got.CustomDomainVerifiedAt)

	r.Equal(1, mustCount(t, s, org.UID))

	// Clear resets every column to NULL/0.
	r.NoError(s.UpdateStatusPageCustomDomain(ctx, page.UID, &models.StatusPageCustomDomainUpdate{}))
	got, err = s.GetStatusPage(ctx, org.UID, page.UID)
	r.NoError(err)
	r.Nil(got.CustomDomain)
	r.Nil(got.CustomDomainVerifiedAt)
	r.Equal(0, got.CustomDomainFailures)
	r.Equal(0, mustCount(t, s, org.UID))
}

func TestCustomDomain_GlobalUniqueIndex(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s := newCustomDomainTestService(t)

	// Two pages in DIFFERENT orgs cannot claim the same domain (the index is
	// global, not per-org).
	orgA := models.NewOrganization("cd-a", "A")
	r.NoError(s.CreateOrganization(ctx, orgA))
	orgB := models.NewOrganization("cd-b", "B")
	r.NoError(s.CreateOrganization(ctx, orgB))

	pageA := models.NewStatusPage(orgA.UID, "A", "main")
	r.NoError(s.CreateStatusPage(ctx, pageA))
	pageB := models.NewStatusPage(orgB.UID, "B", "main")
	r.NoError(s.CreateStatusPage(ctx, pageB))

	domain := "status.shared.com"
	tok := "t"
	r.NoError(s.UpdateStatusPageCustomDomain(ctx, pageA.UID, &models.StatusPageCustomDomainUpdate{
		Domain: &domain, Token: &tok,
	}))

	err := s.UpdateStatusPageCustomDomain(ctx, pageB.UID, &models.StatusPageCustomDomainUpdate{
		Domain: &domain, Token: &tok,
	})
	r.Error(err, "the global unique index must reject a second claim of the same domain")
}

func TestListStatusPagesWithCustomDomain(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s := newCustomDomainTestService(t)

	org := models.NewOrganization("cd-list", "L")
	r.NoError(s.CreateOrganization(ctx, org))

	withDomain := models.NewStatusPage(org.UID, "A", "with")
	d := "d.example.com"
	tk := "t"
	withDomain.CustomDomain = &d
	withDomain.CustomDomainToken = &tk
	r.NoError(s.CreateStatusPage(ctx, withDomain))

	without := models.NewStatusPage(org.UID, "B", "without")
	r.NoError(s.CreateStatusPage(ctx, without))

	pages, err := s.ListStatusPagesWithCustomDomain(ctx)
	r.NoError(err)
	r.Len(pages, 1)
	r.Equal(withDomain.UID, pages[0].UID)
}

func mustCount(t *testing.T, s *Service, orgUID string) int {
	t.Helper()
	n, err := s.CountStatusPagesWithCustomDomain(t.Context(), orgUID)
	require.NoError(t, err)

	return n
}
