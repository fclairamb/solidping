package statuspages

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestCreateStatusPage_WithoutCheckUIDs_SeedsDefaultSection pins the base
// case of spec 2026-08-28-16: every new page gets a "Services" section at
// position 0, with zero resources, even when checkUids is absent.
func TestCreateStatusPage_WithoutCheckUIDs_SeedsDefaultSection(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{Name: "Public", Slug: testPublicSlug})
	r.NoError(err)

	sections, err := svc.db.ListStatusPageSections(ctx, page.UID)
	r.NoError(err)
	r.Len(sections, 1)
	r.Equal("Services", sections[0].Name)
	r.Equal("services", sections[0].Slug)
	r.Equal(0, sections[0].Position)

	resources, err := svc.db.ListStatusPageResources(ctx, sections[0].UID)
	r.NoError(err)
	r.Empty(resources)
}

// TestCreateStatusPage_WithCheckUIDs_SeedsDefaultSectionAndResources pins the
// happy path: checkUids yields one resource per check, in request order,
// inside the seeded "Services" section.
func TestCreateStatusPage_WithCheckUIDs_SeedsDefaultSectionAndResources(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	checks := make([]*models.Check, 3)
	for i := range checks {
		c := models.NewCheck(org.UID, "", "http")
		r.NoError(svc.db.CreateCheck(ctx, c))
		checks[i] = c
	}

	checkUIDs := []string{checks[2].UID, checks[0].UID, checks[1].UID}

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Public", Slug: testPublicSlug, CheckUIDs: checkUIDs,
	})
	r.NoError(err)

	sections, err := svc.db.ListStatusPageSections(ctx, page.UID)
	r.NoError(err)
	r.Len(sections, 1)
	r.Equal("Services", sections[0].Name)
	r.Equal("services", sections[0].Slug)

	resources, err := svc.db.ListStatusPageResources(ctx, sections[0].UID)
	r.NoError(err)
	r.Len(resources, 3)

	for i, uid := range checkUIDs {
		r.NotNil(resources[i].CheckUID)
		r.Equal(uid, *resources[i].CheckUID, "resource %d should target the request-order check", i)
		r.Equal(i, resources[i].Position, "resource %d should carry position %d", i, i)
	}
}

// TestCreateStatusPage_UnknownCheckUID_RejectsAtomically pins the hard
// atomicity requirement: an unknown checkUid rejects the WHOLE request, and
// the page must not exist afterwards — no half-created page.
func TestCreateStatusPage_UnknownCheckUID_RejectsAtomically(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	goodCheck := models.NewCheck(org.UID, "", "http")
	r.NoError(svc.db.CreateCheck(ctx, goodCheck))

	const unknownUID = "00000000-0000-0000-0000-000000000000"

	_, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Public", Slug: testPublicSlug, CheckUIDs: []string{goodCheck.UID, unknownUID},
	})
	r.ErrorIs(err, ErrCheckUIDInvalid)
	r.Contains(err.Error(), unknownUID, "the offending uid must be named in the error")

	// The page must not exist — not by slug, not at all.
	_, errGet := svc.db.GetStatusPageBySlug(ctx, org.UID, testPublicSlug)
	r.Error(errGet, "a rejected create must not leave a page behind")

	pages, err := svc.db.ListStatusPages(ctx, org.UID)
	r.NoError(err)
	r.Empty(pages, "no page, and therefore no section or resource, may have been persisted")
}

// TestCreateStatusPage_ForeignOrgCheckUID_RejectsAtomically is the
// cross-tenant variant: a checkUid that belongs to a DIFFERENT organization
// must be treated exactly like an unknown one — rejected, page not created.
func TestCreateStatusPage_ForeignOrgCheckUID_RejectsAtomically(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	otherOrg := models.NewOrganization("acme-other", "Acme Other")
	r.NoError(svc.db.CreateOrganization(ctx, otherOrg))

	foreignCheck := models.NewCheck(otherOrg.UID, "", "http")
	r.NoError(svc.db.CreateCheck(ctx, foreignCheck))

	_, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Public", Slug: testPublicSlug, CheckUIDs: []string{foreignCheck.UID},
	})
	r.ErrorIs(err, ErrCheckUIDInvalid)
	r.Contains(err.Error(), foreignCheck.UID)

	_, errGet := svc.db.GetStatusPageBySlug(ctx, org.UID, testPublicSlug)
	r.Error(errGet, "a rejected create must not leave a page behind")
}
