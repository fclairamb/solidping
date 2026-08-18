package severities_test

import (
	"strings"
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

// TestSlugLengthBoundaries pins the 3-100 char slug length window (raised
// from 3-40): 2 chars rejected, 3 accepted, 100 accepted, 101 rejected.
func TestSlugLengthBoundaries(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newSetup(t)

	_, err := s.svc.CreateSeverity(t.Context(), s.org.Slug, severities.CreateSeverityRequest{
		Slug:     "ab",
		Name:     "Too Short",
		Channels: []string{"email"},
	})
	r.ErrorIs(err, severities.ErrInvalidSlug, "2-char slug must be rejected")

	_, err = s.svc.CreateSeverity(t.Context(), s.org.Slug, severities.CreateSeverityRequest{
		Slug:     "abc",
		Name:     "Min Length",
		Channels: []string{"email"},
	})
	r.NoError(err, "3-char slug must be accepted")

	_, err = s.svc.CreateSeverity(t.Context(), s.org.Slug, severities.CreateSeverityRequest{
		Slug:     "a" + strings.Repeat("b", 99),
		Name:     "Max Length",
		Channels: []string{"email"},
	})
	r.NoError(err, "100-char slug must be accepted")

	_, err = s.svc.CreateSeverity(t.Context(), s.org.Slug, severities.CreateSeverityRequest{
		Slug:     "a" + strings.Repeat("b", 100),
		Name:     "Too Long",
		Channels: []string{"email"},
	})
	r.ErrorIs(err, severities.ErrInvalidSlug, "101-char slug must be rejected")
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

// TestCreateAcceptsWhatsAppChannel proves the whatsapp severity token passes
// validation (it is a synthetic direct-channel token, not a connection type),
// with a negative control so the test cannot pass on a validator that accepts
// everything.
func TestCreateAcceptsWhatsAppChannel(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newSetup(t)

	sev, err := s.svc.CreateSeverity(t.Context(), s.org.Slug, severities.CreateSeverityRequest{
		Slug:     "wake-me",
		Name:     "Wake me",
		Channels: []string{"email", "sms", "whatsapp"},
	})
	r.NoError(err)
	r.Contains(sev.Channels, "whatsapp")

	// Negative control: a near-miss spelling must still be rejected.
	_, err = s.svc.CreateSeverity(t.Context(), s.org.Slug, severities.CreateSeverityRequest{
		Slug:     "wake-me-2",
		Name:     "Wake me 2",
		Channels: []string{"whats_app"},
	})
	r.ErrorIs(err, severities.ErrInvalidChannel)
}

// TestCreateAcceptsMatrixChannel proves the matrix connection type passes
// severity-channel validation, with a negative control so the test cannot
// pass on a validator that accepts everything.
func TestCreateAcceptsMatrixChannel(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newSetup(t)

	sev, err := s.svc.CreateSeverity(t.Context(), s.org.Slug, severities.CreateSeverityRequest{
		Slug:     "wake-me-matrix",
		Name:     "Wake me via Matrix",
		Channels: []string{"email", "matrix"},
	})
	r.NoError(err)
	r.Contains(sev.Channels, "matrix")

	// Negative control: a near-miss spelling must still be rejected.
	_, err = s.svc.CreateSeverity(t.Context(), s.org.Slug, severities.CreateSeverityRequest{
		Slug:     "wake-me-matrix-2",
		Name:     "Wake me via Matrix 2",
		Channels: []string{"matrixx"},
	})
	r.ErrorIs(err, severities.ErrInvalidChannel)
}
