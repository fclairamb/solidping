package statuspages

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// --- Public status page overall status (spec 2026-08-08-05) ---
//
// These pin that the public ViewStatusPage payload carries a server-computed
// overallStatus (and matching statusCounts), so status0 no longer has to
// reimplement the rollup client-side.

// resourceSpec is one resource to seed for a TestViewStatusPage_OverallStatus
// test case: a check with the given status, optionally wrapped in an active
// maintenance window.
type resourceSpec struct {
	slug          string
	status        models.CheckStatus
	inMaintenance bool
}

// seedResourceWithStatus creates a check with the given status, attaches it
// to the section as a resource, and — if inMaintenance is true — wraps it in
// an active maintenance window.
func seedResourceWithStatus(
	ctx context.Context, t *testing.T, svc *Service, orgUID, orgSlug string,
	page StatusPageResponse, section StatusPageSectionResponse, spec resourceSpec,
) {
	t.Helper()

	r := require.New(t)

	check := models.NewCheck(orgUID, spec.slug, "http")
	check.Status = spec.status
	r.NoError(svc.db.CreateCheck(ctx, check))

	_, err := svc.CreateResource(ctx, orgSlug, page.UID, section.UID, CreateResourceRequest{CheckUID: check.UID})
	r.NoError(err)

	if spec.inMaintenance {
		win := models.NewMaintenanceWindow(orgUID, "Planned", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
		r.NoError(svc.db.CreateMaintenanceWindow(ctx, win))
		r.NoError(svc.db.SetMaintenanceWindowChecks(ctx, win.UID, []string{check.UID}, nil))
	}
}

// TestViewStatusPage_OverallStatus drives the public view across the full
// state matrix from the spec's rollup rules and pins the resulting
// overallStatus + statusCounts on the wire payload.
func TestViewStatusPage_OverallStatus(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		resources []resourceSpec
		want      string
		wantCnt   StatusCountsResponse
	}{
		{
			name:      "no resources at all -> unknown",
			resources: nil,
			want:      "unknown",
			wantCnt:   StatusCountsResponse{},
		},
		{
			name: "all resources unknown/created -> unknown",
			resources: []resourceSpec{
				{slug: "res-a", status: models.CheckStatusCreated},
				{slug: "res-b", status: models.CheckStatusCreated},
			},
			want:    "unknown",
			wantCnt: StatusCountsResponse{Unknown: 2},
		},
		{
			name: "positive control: one up among unknowns -> operational",
			resources: []resourceSpec{
				{slug: "res-a", status: models.CheckStatusCreated},
				{slug: "res-b", status: models.CheckStatusUp},
			},
			want:    "operational",
			wantCnt: StatusCountsResponse{Unknown: 1, Operational: 1},
		},
		{
			name: "all up -> operational",
			resources: []resourceSpec{
				{slug: "res-a", status: models.CheckStatusUp},
			},
			want:    "operational",
			wantCnt: StatusCountsResponse{Operational: 1},
		},
		{
			name: "one down -> down",
			resources: []resourceSpec{
				{slug: "res-a", status: models.CheckStatusUp},
				{slug: "res-b", status: models.CheckStatusDown},
			},
			want:    "down",
			wantCnt: StatusCountsResponse{Operational: 1, Down: 1},
		},
		{
			name: "one degraded, no down -> degraded",
			resources: []resourceSpec{
				{slug: "res-a", status: models.CheckStatusUp},
				{slug: "res-b", status: models.CheckStatusDegraded},
			},
			want:    "degraded",
			wantCnt: StatusCountsResponse{Operational: 1, Degraded: 1},
		},
		{
			name: "one warning, no down -> degraded",
			resources: []resourceSpec{
				{slug: "res-a", status: models.CheckStatusUp},
				{slug: "res-b", status: models.CheckStatusWarning},
			},
			want:    "degraded",
			wantCnt: StatusCountsResponse{Operational: 1, Degraded: 1},
		},
		{
			name: "maintenance-masks-down: a resource down INSIDE its maintenance window never taints the page",
			resources: []resourceSpec{
				{slug: "res-a", status: models.CheckStatusDown, inMaintenance: true},
			},
			want:    "maintenance",
			wantCnt: StatusCountsResponse{Maintenance: 1},
		},
		{
			name: "maintenance masks its own down resource, but a SEPARATE non-maintenance down resource still wins",
			resources: []resourceSpec{
				{slug: "res-a", status: models.CheckStatusDown, inMaintenance: true},
				{slug: "res-b", status: models.CheckStatusDown},
			},
			want:    "down",
			wantCnt: StatusCountsResponse{Maintenance: 1, Down: 1},
		},
		{
			name: "maintenance with otherwise-clean resources -> maintenance",
			resources: []resourceSpec{
				{slug: "res-a", status: models.CheckStatusUp},
				{slug: "res-b", status: models.CheckStatusUp, inMaintenance: true},
			},
			want:    "maintenance",
			wantCnt: StatusCountsResponse{Operational: 1, Maintenance: 1},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx, svc, org := setupStatusPagesTest(t)
			page, section := seedPageWithSection(ctx, t, svc, org.Slug, "")

			for _, spec := range tc.resources {
				seedResourceWithStatus(ctx, t, svc, org.UID, org.Slug, page, section, spec)
			}

			view, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug)
			r.NoError(err)
			r.Equal(tc.want, view.OverallStatus)
			r.NotNil(view.StatusCounts)
			r.Equal(tc.wantCnt, *view.StatusCounts)
		})
	}
}

// TestViewDefaultStatusPage_OverallStatus pins that the default-page view
// path (which delegates to ViewStatusPage) also carries overallStatus.
func TestViewDefaultStatusPage_OverallStatus(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)
	page, section := seedPageWithSection(ctx, t, svc, org.Slug, "")
	r.True(page.IsDefault, "the first page created for an org is the default page")

	seedResourceWithStatus(ctx, t, svc, org.UID, org.Slug, page, section,
		resourceSpec{slug: "res-a", status: models.CheckStatusDown})

	view, err := svc.ViewDefaultStatusPage(ctx, org.Slug)
	r.NoError(err)
	r.Equal("down", view.OverallStatus)
	r.NotNil(view.StatusCounts)
	r.Equal(1, view.StatusCounts.Down)
}
