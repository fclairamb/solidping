package postgres

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portCustomDomain is distinct from every other _postgres_test.go embedded port
// in this package (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portCustomDomain = 15457

// TestGetStatusPageByCustomDomain_Postgres proves custom-domain lookup, the
// soft-delete filter, and the global partial unique index behave on Postgres.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres pwfile extraction) with its siblings
func TestGetStatusPageByCustomDomain_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{
		Embedded: true,
		Port:     portCustomDomain,
		RunMode:  runModeTest,
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	orgA := models.NewOrganization("cd-pg-a", "A")
	r.NoError(s.CreateOrganization(ctx, orgA))
	orgB := models.NewOrganization("cd-pg-b", "B")
	r.NoError(s.CreateOrganization(ctx, orgB))

	page := models.NewStatusPage(orgA.UID, "Acme", "main")
	domain := "status.acme.example"
	token := "tok"
	page.CustomDomain = &domain
	page.CustomDomainToken = &token
	r.NoError(s.CreateStatusPage(ctx, page))

	got, err := s.GetStatusPageByCustomDomain(ctx, domain)
	r.NoError(err)
	r.Equal(page.UID, got.UID)

	r.Equal(1, mustCountPG(t, s, orgA.UID))

	// Global unique index: a different org cannot claim the same domain.
	pageB := models.NewStatusPage(orgB.UID, "B", "main")
	r.NoError(s.CreateStatusPage(ctx, pageB))
	err = s.UpdateStatusPageCustomDomain(ctx, pageB.UID, &models.StatusPageCustomDomainUpdate{
		Domain: &domain, Token: &token,
	})
	r.Error(err, "the global unique index must reject a duplicate domain")

	// Soft-deleted pages are excluded from lookup.
	r.NoError(s.DeleteStatusPage(ctx, page.UID))
	_, err = s.GetStatusPageByCustomDomain(ctx, domain)
	r.ErrorIs(err, sql.ErrNoRows)
}

func mustCountPG(t *testing.T, s *Service, orgUID string) int {
	t.Helper()
	n, err := s.CountStatusPagesWithCustomDomain(t.Context(), orgUID)
	require.NoError(t, err)

	return n
}
