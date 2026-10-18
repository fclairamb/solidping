package checks_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// liveWorker registers a live cloud worker in region with the given
// capability set.
func (e *warnEnv) liveWorker(slug, region string, capabilities []string) {
	e.t.Helper()

	r := require.New(e.t)
	ctx := e.t.Context()

	worker, err := e.dbSvc.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID: uuid.New().String(), Slug: slug, Name: slug, Region: &region, Capabilities: capabilities,
	})
	r.NoError(err)

	_, err = e.dbSvc.DB().NewUpdate().Model((*models.Worker)(nil)).
		Set("last_active_at = ?", time.Now()).
		Where("uid = ?", worker.UID).Exec(ctx)
	r.NoError(err)
}

// A plugin-type check in a region whose live workers do not advertise the
// plugin is saved with a REGION_NO_PLUGIN warning (spec 2026-10-17-05 §17).
func TestPluginCheckInRegionWithoutThePluginWarns(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	const checkType = "x-checks-region"

	loadTestPlugin(t, checkType)

	env := newWarnEnv(t)
	env.setRegions([]regions.RegionDefinition{
		{Slug: "eu", Emoji: "🇪🇺", Name: "Europe"},
		{Slug: "us", Emoji: "🇺🇸", Name: "United States"},
	})
	env.liveWorker("wrk-eu", "eu-west-1", []string{"ipv4"})
	env.liveWorker("wrk-us", "us-east-1", []string{"ipv4", checkType})

	resp, err := env.svc.CreateCheck(ctx, env.org.Slug, checks.CreateCheckRequest{
		Name: "No plugin", Slug: "no-plugin", Type: checkType,
		Config: map[string]any{"host": "gopher.acme.com"}, Regions: []string{"eu"},
	})
	r.NoError(err, "a warning never rejects the write")
	r.Len(resp.Warnings, 1)
	r.Equal(checks.CodeRegionNoPlugin, resp.Warnings[0].Code)
	r.Contains(resp.Warnings[0].Message, "Europe")

	// Positive control: the region whose worker has the plugin does not warn.
	resp, err = env.svc.CreateCheck(ctx, env.org.Slug, checks.CreateCheckRequest{
		Name: "With plugin", Slug: "with-plugin", Type: checkType,
		Config: map[string]any{"host": "gopher.acme.com"}, Regions: []string{"us"},
	})
	r.NoError(err)
	r.Empty(resp.Warnings)
}
