package checks

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestFoldCheckStatusCounts pins the status mapping — the correctness-sensitive
// half of the spec — without a database. The expectations mirror, line for
// line, what web/dash0/src/components/dashboard/dashboard-page.tsx derives:
//
//	effectiveStatus(c)  = c.status ?? c.lastResult?.status  (c.status is always set)
//	isDownStatus(s)     = s === "down" || s === "error" || s === "timeout"
//	isHardDownStatus(s) = s === "down" || s === "error"
//	enabledCount        = checks.filter(c => c.enabled !== false).length
//	disabledCount       = checks.length - enabledCount
func TestFoldCheckStatusCounts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rows []models.CheckStatusCount
		want CheckStatsResponse
	}{
		{
			name: "no rows yields an all-zero snapshot with every status key present",
			rows: nil,
			want: CheckStatsResponse{},
		},
		{
			name: "same status split across enabled and disabled is summed per status",
			rows: []models.CheckStatusCount{
				{Status: models.CheckStatusUp, Enabled: true, Count: 10},
				{Status: models.CheckStatusUp, Enabled: false, Count: 4},
			},
			want: CheckStatsResponse{Total: 14, Enabled: 10, Disabled: 4},
		},
		{
			name: "down counts toward both down and hardDown, enabled or not",
			rows: []models.CheckStatusCount{
				{Status: models.CheckStatusDown, Enabled: true, Count: 3},
				{Status: models.CheckStatusDown, Enabled: false, Count: 2},
			},
			want: CheckStatsResponse{Total: 5, Enabled: 3, Disabled: 2, Down: 5, HardDown: 5},
		},
		{
			name: "warning, validating, degraded and created are never counted as down",
			rows: []models.CheckStatusCount{
				{Status: models.CheckStatusWarning, Enabled: true, Count: 2},
				{Status: models.CheckStatusValidating, Enabled: true, Count: 3},
				{Status: models.CheckStatusDegraded, Enabled: true, Count: 4},
				{Status: models.CheckStatusCreated, Enabled: true, Count: 5},
			},
			want: CheckStatsResponse{Total: 14, Enabled: 14, Down: 0, HardDown: 0},
		},
		{
			name: "an unrecognized status column value lands in the unknown bucket, not down",
			rows: []models.CheckStatusCount{
				{Status: models.CheckStatus(99), Enabled: true, Count: 2},
			},
			want: CheckStatsResponse{Total: 2, Enabled: 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			got := foldCheckStatusCounts(tt.rows)

			r.Equal(tt.want.Total, got.Total)
			r.Equal(tt.want.Enabled, got.Enabled)
			r.Equal(tt.want.Disabled, got.Disabled)
			r.Equal(tt.want.Down, got.Down)
			r.Equal(tt.want.HardDown, got.HardDown)

			// Every known status key is always present, and the buckets sum
			// back to the total — no row may be dropped by the fold.
			r.Len(got.ByStatus, len(checkStatusNames()))

			sum := 0
			for _, name := range checkStatusNames() {
				count, ok := got.ByStatus[name]
				r.True(ok, "byStatus missing key %q", name)
				sum += count
			}

			r.Equal(got.Total, sum, "byStatus buckets must sum to total")
		})
	}
}

// TestUnknownStatusIsBucketedAsUnknown makes the unknown-bucket claim explicit:
// a status int outside the known set must land under "unknown".
func TestUnknownStatusIsBucketedAsUnknown(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	got := foldCheckStatusCounts([]models.CheckStatusCount{
		{Status: models.CheckStatus(99), Enabled: true, Count: 7},
	})

	r.Equal(7, got.ByStatus[models.WireStatusUnknown])
	r.Zero(got.Down)
	r.Zero(got.HardDown)
}

// TestDownStatusHelpersMirrorFrontend documents (and locks in) the exact
// predicate parity with the dash0 helpers, including the result-only statuses
// that a check-level status can never actually hold.
func TestDownStatusHelpersMirrorFrontend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status       string
		wantDown     bool
		wantHardDown bool
	}{
		{models.WireStatusUp, false, false},
		{models.WireStatusCreated, false, false},
		{models.WireStatusValidating, false, false},
		{models.WireStatusDegraded, false, false},
		{models.WireStatusWarning, false, false},
		{models.WireStatusUnknown, false, false},
		{models.WireStatusDown, true, true},
		// Result-level statuses: unreachable from a check row today, but the
		// predicates mirror the frontend's, which does test for them.
		{models.WireStatusError, true, true},
		{models.WireStatusTimeout, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			r.Equal(tt.wantDown, isDownWireStatus(tt.status))
			r.Equal(tt.wantHardDown, isHardDownWireStatus(tt.status))
		})
	}
}

// TestCheckStatsCacheTTLBoundary drives the cache directly: a hit inside the
// TTL, a miss once the entry is older than it.
func TestCheckStatsCacheTTLBoundary(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cache := &checkStatsCache{}
	cache.put("org-1", CheckStatsResponse{Total: 5, ByStatus: map[string]int{models.WireStatusUp: 5}})

	got, ok := cache.get("org-1", time.Hour)
	r.True(ok, "a fresh entry must be a hit")
	r.Equal(5, got.Total)

	_, ok = cache.get("org-2", time.Hour)
	r.False(ok, "an unseeded org must be a miss")

	// Age the entry past the TTL without sleeping.
	cache.mu.Lock()
	entry := cache.entries["org-1"]
	entry.computedAt = time.Now().Add(-2 * time.Hour)
	cache.entries["org-1"] = entry
	cache.mu.Unlock()

	_, ok = cache.get("org-1", time.Hour)
	r.False(ok, "an entry older than the TTL must be a miss")
}
