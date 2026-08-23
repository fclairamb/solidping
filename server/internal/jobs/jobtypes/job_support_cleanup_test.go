package jobtypes

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestSupportRetentionFromParam pins the one value the shared retention helper
// would have silently destroyed: ZERO.
//
// resolveRetentionTier rejects anything below 1 and falls through to the
// hardcoded default, so reusing it would have turned "keep forever" — the
// switch an operator under a legal hold needs — into "delete after 365 days".
// That is the opposite of what the setting says, and it would only be
// discovered a year later by the records going missing.
func TestSupportRetentionFromParam(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// A JSON round trip yields float64; a directly-set parameter yields int.
	days, ok := supportRetentionFromParam(models.JSONMap{"value": float64(30)})
	r.True(ok)
	r.Equal(30, days)

	days, ok = supportRetentionFromParam(models.JSONMap{"value": 90})
	r.True(ok)
	r.Equal(90, days)

	// Zero is VALID and means "keep forever" — the whole reason this helper
	// exists rather than the shared one.
	days, ok = supportRetentionFromParam(models.JSONMap{"value": float64(0)})
	r.True(ok)
	r.Equal(0, days)

	// Negative and unparseable values are rejected so the caller falls back to
	// the default rather than purging on a typo.
	_, ok = supportRetentionFromParam(models.JSONMap{"value": float64(-1)})
	r.False(ok)

	_, ok = supportRetentionFromParam(models.JSONMap{"value": "forever"})
	r.False(ok)

	_, ok = supportRetentionFromParam(models.JSONMap{})
	r.False(ok)
}

// TestSupportCleanupDefaults documents the shipped retention period, which the
// docs page and the privacy text both quote.
func TestSupportCleanupDefaults(t *testing.T) {
	t.Parallel()

	require.Equal(t, 365, DefaultSupportRetentionDays,
		"the docs and the privacy text both say twelve months")
}
