package jobtypes_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
)

// TestNetworkDiscoveryChildRollsUpUnderParent verifies a child scan (one carrying
// parentJobUid) persists its discovered hosts under the parent plan's UID, not its
// own, so the scan-detail page sees every chunk's hosts under one UID.
func TestNetworkDiscoveryChildRollsUpUnderParent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("child-disc", "Child Discovery Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// Parent plan job (provides the rollup UID) + the executing child job row.
	plan := models.NewJob(&org.UID, string(jobdef.JobTypeNetworkDiscoveryPlan))
	r.NoError(dbSvc.CreateJob(ctx, plan))

	child := models.NewJob(&org.UID, string(jobdef.JobTypeNetworkDiscovery))
	r.NoError(dbSvc.CreateJob(ctx, child))

	// Listen on a default scanner port so 127.0.0.1 is found responsive.
	_, openPort := listenOnDefaultPort(t)

	def := &jobtypes.NetworkDiscoveryJobDefinition{}
	cfgBytes, err := json.Marshal(disc.Config{
		CIDRs:        []string{"127.0.0.1/32"},
		ParentJobUID: plan.UID,
	})
	r.NoError(err)

	runner, err := def.CreateJobRun(cfgBytes)
	r.NoError(err)

	jctx := &jobdef.JobContext{
		OrganizationUID: &org.UID,
		Job:             child,
		DB:              dbSvc.DB(),
		DBService:       dbSvc,
		Services:        &services.Registry{},
		Logger:          slog.Default(),
	}

	r.NoError(runner.Run(context.Background(), jctx))

	var hosts []*models.DiscoveredHost
	r.NoError(dbSvc.DB().NewSelect().
		Model(&hosts).
		Where("organization_uid = ?", org.UID).
		Where("deleted_at IS NULL").
		Scan(ctx))

	r.Len(hosts, 1)
	r.Equal("127.0.0.1", hosts[0].IP)
	// The host rolls up under the PLAN UID, not the child job's own UID.
	r.Equal(plan.UID, hosts[0].JobUID)
	r.NotEqual(child.UID, hosts[0].JobUID)
	r.Contains(string(hosts[0].OpenPorts), strconv.Itoa(openPort))
}
