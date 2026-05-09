package severities_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/severities"
)

// setup spins up an in-memory sqlite, an org, and the severities service.
type setup struct {
	svc   *severities.Service
	dbSvc *sqlite.Service
	org   *models.Organization
}

func newSetup(t *testing.T) *setup {
	t.Helper()
	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	svc := severities.NewService(dbSvc)

	org := models.NewOrganization("sev-test", "Severity Test")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	return &setup{svc: svc, dbSvc: dbSvc, org: org}
}

// TestSeedDefaultsCreatesThreeRows pins the seed contract: every freshly
// seeded org gets the low/default/critical trio with default = the
// "default"-slug row.
func TestSeedDefaultsCreatesThreeRows(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newSetup(t)

	r.NoError(s.svc.SeedDefaults(t.Context(), s.org.UID))

	list, err := s.svc.ListSeverities(t.Context(), s.org.Slug)
	r.NoError(err)
	r.Len(list, 3, "three default severities seeded")

	slugs := make(map[string]bool)
	var defaultCount int
	for _, sev := range list {
		slugs[sev.Slug] = true
		if sev.IsDefault {
			defaultCount++
			r.Equal("default", sev.Slug,
				"the row marked default must be the one with slug 'default'")
		}
	}
	r.True(slugs["low"])
	r.True(slugs["default"])
	r.True(slugs["critical"])
	r.Equal(1, defaultCount, "exactly one row must carry is_default")
}

// TestCreateRejectsBadChannel pins channel-array validation: unknown
// channel-types are rejected with ErrInvalidChannel.
func TestCreateRejectsBadChannel(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newSetup(t)

	_, err := s.svc.CreateSeverity(t.Context(), s.org.Slug, severities.CreateSeverityRequest{
		Slug:     "bogus",
		Name:     "Bogus",
		Channels: []string{"email", "carrier_pigeon"},
	})
	r.ErrorIs(err, severities.ErrInvalidChannel,
		"unknown channel type must be rejected")
}

// TestCreateRejectsBadSlug pins slug formatting.
func TestCreateRejectsBadSlug(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newSetup(t)

	_, err := s.svc.CreateSeverity(t.Context(), s.org.Slug, severities.CreateSeverityRequest{
		Slug:     "Has Spaces",
		Name:     "Bad",
		Channels: []string{"email"},
	})
	r.ErrorIs(err, severities.ErrInvalidSlug)
}

// TestCreateRejectsDuplicateSlug pins the org-scoped slug uniqueness.
func TestCreateRejectsDuplicateSlug(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newSetup(t)

	req := severities.CreateSeverityRequest{
		Slug:     "ops",
		Name:     "Ops",
		Channels: []string{"email"},
	}
	_, err := s.svc.CreateSeverity(t.Context(), s.org.Slug, req)
	r.NoError(err)

	_, err = s.svc.CreateSeverity(t.Context(), s.org.Slug, req)
	r.ErrorIs(err, severities.ErrSlugConflict)
}

// TestPromoteDefaultDemotesPrevious pins the unique-default invariant:
// promoting a new severity to default flips the previous default off.
func TestPromoteDefaultDemotesPrevious(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newSetup(t)

	r.NoError(s.svc.SeedDefaults(t.Context(), s.org.UID))

	newDefault := true
	updated, err := s.svc.UpdateSeverity(t.Context(), s.org.Slug, "critical", severities.UpdateSeverityRequest{
		IsDefault: &newDefault,
	})
	r.NoError(err)
	r.True(updated.IsDefault)

	all, err := s.svc.ListSeverities(t.Context(), s.org.Slug)
	r.NoError(err)
	var defaults int
	for _, sev := range all {
		if sev.IsDefault {
			defaults++
			r.Equal("critical", sev.Slug, "critical is the new default")
		}
	}
	r.Equal(1, defaults, "exactly one default after promotion")
}

// TestDeleteDefaultIsRefused pins the safety: operators must promote
// another severity first before deleting the current default.
func TestDeleteDefaultIsRefused(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newSetup(t)

	r.NoError(s.svc.SeedDefaults(t.Context(), s.org.UID))

	err := s.svc.DeleteSeverity(t.Context(), s.org.Slug, "default")
	r.ErrorIs(err, severities.ErrCannotDeleteDefault)
}

// TestDeleteSucceedsForNonDefault confirms the happy delete path.
func TestDeleteSucceedsForNonDefault(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newSetup(t)

	r.NoError(s.svc.SeedDefaults(t.Context(), s.org.UID))

	r.NoError(s.svc.DeleteSeverity(t.Context(), s.org.Slug, "low"))

	_, err := s.svc.GetSeverity(t.Context(), s.org.Slug, "low")
	r.ErrorIs(err, severities.ErrSeverityNotFound)
}

// TestSeedDefaultsIsIdempotent confirms re-running the seed on an org
// that already has the rows is a no-op.
func TestSeedDefaultsIsIdempotent(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newSetup(t)

	r.NoError(s.svc.SeedDefaults(t.Context(), s.org.UID))
	r.NoError(s.svc.SeedDefaults(t.Context(), s.org.UID))

	list, err := s.svc.ListSeverities(t.Context(), s.org.Slug)
	r.NoError(err)
	r.Len(list, 3, "double-seed must not create duplicates")
}
