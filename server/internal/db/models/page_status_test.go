package models_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestRollupPageStatus covers every rule of the spec 2026-08-08-05 page-level
// rollup, in priority order, including the two cases the spec calls out
// explicitly: maintenance masking a down resource, and the all-unknown case
// with a positive control (one up resource among unknowns flips the page to
// operational).
func TestRollupPageStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		resources  []models.PageResourceStatus
		wantStatus models.PageStatus
		wantCounts models.PageStatusCounts
	}{
		{
			name:       "no resources at all",
			resources:  nil,
			wantStatus: models.PageStatusUnknown,
			wantCounts: models.PageStatusCounts{},
		},
		{
			name: "all unknown (created), no positive control",
			resources: []models.PageResourceStatus{
				{Status: models.CheckStatusCreated},
				{Status: models.CheckStatusCreated},
			},
			wantStatus: models.PageStatusUnknown,
			wantCounts: models.PageStatusCounts{Unknown: 2},
		},
		{
			name: "all unknown WITH a positive control - one up flips to operational",
			resources: []models.PageResourceStatus{
				{Status: models.CheckStatusCreated},
				{Status: models.CheckStatusCreated},
				{Status: models.CheckStatusUp},
			},
			wantStatus: models.PageStatusOperational,
			wantCounts: models.PageStatusCounts{Unknown: 2, Operational: 1},
		},
		{
			name: "single up resource - operational",
			resources: []models.PageResourceStatus{
				{Status: models.CheckStatusUp},
			},
			wantStatus: models.PageStatusOperational,
			wantCounts: models.PageStatusCounts{Operational: 1},
		},
		{
			name: "validating counts as usable/operational",
			resources: []models.PageResourceStatus{
				{Status: models.CheckStatusValidating},
			},
			wantStatus: models.PageStatusOperational,
			wantCounts: models.PageStatusCounts{Operational: 1},
		},
		{
			name: "single down resource - down",
			resources: []models.PageResourceStatus{
				{Status: models.CheckStatusUp},
				{Status: models.CheckStatusDown},
			},
			wantStatus: models.PageStatusDown,
			wantCounts: models.PageStatusCounts{Operational: 1, Down: 1},
		},
		{
			name: "degraded resource, no down - degraded",
			resources: []models.PageResourceStatus{
				{Status: models.CheckStatusUp},
				{Status: models.CheckStatusDegraded},
			},
			wantStatus: models.PageStatusDegraded,
			wantCounts: models.PageStatusCounts{Operational: 1, Degraded: 1},
		},
		{
			name: "warning resource, no down - degraded (warning collapses into degraded)",
			resources: []models.PageResourceStatus{
				{Status: models.CheckStatusUp},
				{Status: models.CheckStatusWarning},
			},
			wantStatus: models.PageStatusDegraded,
			wantCounts: models.PageStatusCounts{Operational: 1, Degraded: 1},
		},
		{
			name: "down wins over degraded",
			resources: []models.PageResourceStatus{
				{Status: models.CheckStatusDown},
				{Status: models.CheckStatusDegraded},
			},
			wantStatus: models.PageStatusDown,
			wantCounts: models.PageStatusCounts{Down: 1, Degraded: 1},
		},
		{
			name: "maintenance masks down - the resource is down but inside its own maintenance window",
			resources: []models.PageResourceStatus{
				{Status: models.CheckStatusDown, InMaintenance: true},
			},
			wantStatus: models.PageStatusMaintenance,
			wantCounts: models.PageStatusCounts{Maintenance: 1},
		},
		{
			name: "maintenance masks down, but a separate non-maintenance down resource still wins",
			resources: []models.PageResourceStatus{
				{Status: models.CheckStatusDown, InMaintenance: true},
				{Status: models.CheckStatusDown},
			},
			wantStatus: models.PageStatusDown,
			wantCounts: models.PageStatusCounts{Maintenance: 1, Down: 1},
		},
		{
			name: "maintenance masks down, but a separate degraded resource still surfaces",
			resources: []models.PageResourceStatus{
				{Status: models.CheckStatusDown, InMaintenance: true},
				{Status: models.CheckStatusDegraded},
			},
			wantStatus: models.PageStatusDegraded,
			wantCounts: models.PageStatusCounts{Maintenance: 1, Degraded: 1},
		},
		{
			name: "maintenance with otherwise-clean resources - maintenance",
			resources: []models.PageResourceStatus{
				{Status: models.CheckStatusUp},
				{Status: models.CheckStatusUp, InMaintenance: true},
			},
			wantStatus: models.PageStatusMaintenance,
			wantCounts: models.PageStatusCounts{Operational: 1, Maintenance: 1},
		},
		{
			name: "only a maintenance resource, otherwise unknown pool - maintenance beats unknown",
			resources: []models.PageResourceStatus{
				{Status: models.CheckStatusCreated, InMaintenance: true},
			},
			wantStatus: models.PageStatusMaintenance,
			wantCounts: models.PageStatusCounts{Maintenance: 1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			gotStatus, gotCounts := models.RollupPageStatus(tc.resources)
			require.Equal(t, tc.wantStatus, gotStatus)
			require.Equal(t, tc.wantCounts, gotCounts)
		})
	}
}
