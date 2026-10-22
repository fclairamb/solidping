package discovery_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

const portDiscoveryRegionPG = 15720

// TestUpsertDiscoveredChecksRegion_Postgres proves the region-aware conflict
// target on Postgres: the same IP found from the server and from @dc1 is two
// rows, and re-scans (either side) update in place instead of duplicating.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres pwfile extraction) with its siblings
func TestUpsertDiscoveredChecksRegion_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: portDiscoveryRegionPG, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	org := models.NewOrganization("disc-region-pg", "Disc")
	r.NoError(svc.CreateOrganization(ctx, org))

	job := models.NewJob(&org.UID, "network_discovery")
	r.NoError(svc.CreateJob(ctx, job))

	rows := []discovery.SuggestedCheck{{
		GroupKey: "10.0.0.5", GroupLabel: "10.0.0.5", Name: "10.0.0.5 · TCP/22", Slug: "tcp-10-0-0-5-22",
		Type: "tcp", Config: json.RawMessage(`{"host":"10.0.0.5","port":22}`),
	}}

	for _, region := range []string{"", "@dc1", "", "@dc1"} {
		r.NoError(discovery.UpsertDiscoveredChecks(
			ctx, svc.DB(), org.UID, job.UID, region, models.DiscoverySourceLAN, rows, nil,
		))
	}

	count, err := svc.DB().NewSelect().TableExpr("discovered_checks").
		Where("organization_uid = ?", org.UID).Count(ctx)
	r.NoError(err)
	r.Equal(2, count)
}
