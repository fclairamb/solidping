package jobtypes_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/egress"
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
	openPort := listenOnDefaultPort(t)

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

	var checks []*models.DiscoveredCheck
	r.NoError(dbSvc.DB().NewSelect().
		Model(&checks).
		Where("organization_uid = ?", org.UID).
		Where("deleted_at IS NULL").
		Scan(ctx))

	r.NotEmpty(checks, "the responsive host must yield at least one suggested check")

	for _, c := range checks {
		// Every check rolls up under the PLAN UID, not the child job's own UID.
		r.Equal(plan.UID, c.JobUID)
		r.NotEqual(child.UID, c.JobUID)
		r.Equal("127.0.0.1", c.GroupKey)
		r.Equal(models.DiscoverySourceLAN, c.Source)
	}

	// The open port shows up in the group metadata (denormalized).
	r.Contains(string(checks[0].Metadata), strconv.Itoa(openPort))
}

// A network_discovery child run under an enforcing process guard probes
// nothing: the loopback listener accepts no connection and no row is
// persisted. The permissive guard is the positive control.
func TestNetworkDiscoveryObeysEgressGuard(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		guard     *egress.Guard
		wantFound bool
	}{
		{name: "enforcing", guard: egress.New(false), wantFound: false},
		{name: "permissive", guard: egress.New(true), wantFound: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx := t.Context()

			dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
			r.NoError(err)
			r.NoError(dbSvc.Initialize(ctx))
			t.Cleanup(func() { _ = dbSvc.Close() })

			org := models.NewOrganization("egress-disc", "Egress Discovery Org")
			r.NoError(dbSvc.CreateOrganization(ctx, org))

			job := models.NewJob(&org.UID, string(jobdef.JobTypeNetworkDiscovery))
			r.NoError(dbSvc.CreateJob(ctx, job))

			// A default scanner port, so a found host yields suggested checks,
			// plus a port of this test's own whose accepted connections are
			// counted (the default ports are shared with other parallel scans).
			openPort := listenOnDefaultPort(t)
			ownPort, accepted, ok := countingListener(t, "127.0.0.1:0")
			r.True(ok)

			cfgBytes, err := json.Marshal(disc.Config{
				CIDRs: []string{"127.0.0.1/32"},
				Ports: []int{openPort, ownPort},
			})
			r.NoError(err)

			runner, err := (&jobtypes.NetworkDiscoveryJobDefinition{}).CreateJobRun(cfgBytes)
			r.NoError(err)

			r.NoError(runner.Run(ctx, &jobdef.JobContext{
				OrganizationUID: &org.UID,
				Job:             job,
				DB:              dbSvc.DB(),
				DBService:       dbSvc,
				Services:        &services.Registry{EgressGuard: tc.guard},
				Logger:          slog.Default(),
			}))

			count, err := dbSvc.DB().NewSelect().Model((*models.DiscoveredCheck)(nil)).
				Where("organization_uid = ?", org.UID).Count(ctx)
			r.NoError(err)

			if !tc.wantFound {
				r.Zero(count, "a refused host must not be persisted")
				r.Zero(accepted.Load(), "a refused host must not be dialed")

				return
			}

			r.Positive(count)
			r.Eventually(func() bool { return accepted.Load() > 0 }, 5*time.Second, 10*time.Millisecond)
		})
	}
}
