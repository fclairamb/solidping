package statuspages

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// A stale component is spoken as "stale" on the public wire — the front end
// renders it as the neutral "No data, last checked …" — and never as "up"
// (spec 2026-09-25-02). A stale GROUP reads the same, since the group rollup
// yields CheckStatusStale for an all-stale (or stale-and-up) group.
func TestPublicCheckStatus_Stale(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("stale", publicCheckStatus(models.CheckStatusStale))
	r.NotEqual(statusUp, publicCheckStatus(models.CheckStatusStale))

	group := models.RollupGroupStatus(map[models.CheckStatus]int{models.CheckStatusStale: 3})
	r.Equal("stale", publicCheckStatus(group))

	// Validating stays hidden as up; created stays created: unchanged.
	r.Equal(statusUp, publicCheckStatus(models.CheckStatusValidating))
	r.Equal(statusCreated, publicCheckStatus(models.CheckStatusCreated))
}
