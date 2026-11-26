package agentws_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/agentws"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// Outdated private-location agent notice (spec 2026-11-26-03).

const (
	outdatedServer = "0.55.0"
	outdatedOld    = "0.49.3"
	outdatedAdmin  = "admin@acme.test"
)

// outdatedEnv is an env whose handler emails the org's admins.
func outdatedEnv(t *testing.T) *env {
	t.Helper()

	var jobs jobsvc.Service

	e := newEnvWrapped(t, nil, func(h *agentws.Handler) {
		h.SetOutdatedNotifier(agentws.OutdatedNotifier{Jobs: jobs, BaseURL: "https://app.acme.test"})
	}, func(d db.Service) db.Service {
		events := notifier.NewLocalEventNotifier()
		t.Cleanup(func() { _ = events.Close() })

		jobs = jobsvc.NewService(d.DB(), d, events, nil)

		return d
	})

	ctx := t.Context()
	user := models.NewUser(outdatedAdmin)
	require.NoError(t, e.dbSvc.CreateUser(ctx, user))
	require.NoError(t, e.dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(e.org.UID, user.UID, models.MemberRoleAdmin)))

	return e
}

// legacyClaim is a claim from an agent that predates the rolling upgrade: it
// reports a version and no autoUpgrade field.
func legacyClaim(id, ver string) agentcrypto.ClientFrame {
	return agentcrypto.ClientFrame{Type: agentcrypto.MsgTypeClaim, ID: id, MaxJobs: 1, Version: ver}
}

// emailJobs counts the queued outdated-agent emails.
func (e *env) emailJobs() int {
	e.t.Helper()

	var jobs []*models.Job

	require.NoError(e.t, e.dbSvc.DB().NewSelect().Model(&jobs).
		Where("type = ?", "email").Scan(e.t.Context()))

	count := 0

	for _, job := range jobs {
		if job.Config["template"] == "agent-outdated.html" {
			count++
		}
	}

	return count
}

// claimOnNewConnection reconnects the agent and sends one claim.
func claimOnNewConnection(
	t *testing.T, e *env, keys *agentcrypto.AgentKeys, agentUID, nonce string, frame agentcrypto.ClientFrame,
) {
	t.Helper()

	conn := reconnect(t, e, keys, agentUID, nonce)
	resp := roundTrip(t, conn, frame)
	require.Equal(t, agentcrypto.MsgTypeJobs, resp.Type)
	require.NoError(t, conn.Close(1000, "done"))
}

func (e *env) workerOf(agentUID string) *models.Worker {
	e.t.Helper()

	workers, err := e.dbSvc.ListWorkers(e.t.Context())
	require.NoError(e.t, err)

	for _, w := range workers {
		if w.Slug == agentUID || w.UID == agentUID || w.Name == agentUID {
			return w
		}
	}

	require.Len(e.t, workers, 1)

	return workers[0]
}

//nolint:paralleltest // mutates version.Version
func TestLegacyAgentIsReportedOnceThenDeduplicated(t *testing.T) {
	withServerVersion(t, outdatedServer)

	r := require.New(t)
	e := outdatedEnv(t)

	conn, keys, enrolled := e.enroll(e.mintToken(), "paris-dc1")
	resp := roundTrip(t, conn, legacyClaim("c1", outdatedOld))
	r.Equal(agentcrypto.MsgTypeJobs, resp.Type)

	events := e.agentEvents(models.EventTypeAgentOutdated)
	r.Len(events, 1)
	r.Equal("paris-dc1", events[0].Payload["agent"])
	r.Equal(outdatedOld, events[0].Payload["version"])
	r.Equal(outdatedServer, events[0].Payload["server_version"])
	r.Equal(true, events[0].Payload["legacy"])
	r.Equal(1, e.emailJobs())

	// Same versions on a second connection: nothing more.
	claimOnNewConnection(t, e, keys, enrolled.AgentUID, "n-2", legacyClaim("c2", outdatedOld))
	r.Len(e.agentEvents(models.EventTypeAgentOutdated), 1)
	r.Equal(1, e.emailJobs())

	// A server upgrade makes it worth telling again.
	withServerVersion(t, "0.56.0")
	claimOnNewConnection(t, e, keys, enrolled.AgentUID, "n-3", legacyClaim("c3", outdatedOld))
	r.Len(e.agentEvents(models.EventTypeAgentOutdated), 2)
	r.Equal(2, e.emailJobs())
}

//nolint:paralleltest // mutates version.Version
func TestDriftedAgentIsReportedOnlyAfterTheGracePeriod(t *testing.T) {
	withServerVersion(t, outdatedServer)

	r := require.New(t)
	e := outdatedEnv(t)

	conn, keys, enrolled := e.enroll(e.mintToken(), "paris-dc1")
	resp := roundTrip(t, conn, followingClaim("c1", outdatedOld))
	r.Equal(agentcrypto.MsgTypeJobs, resp.Type)

	r.Empty(e.agentEvents(models.EventTypeAgentOutdated), "freshly drifted: not yet")
	r.Zero(e.emailJobs())

	worker := e.workerOf("paris-dc1")
	r.NotNil(worker.VersionDriftedSince, "the first drifted report starts the clock")

	// Backdate the clock past 72 h.
	_, err := e.dbSvc.DB().NewUpdate().Model((*models.Worker)(nil)).
		Set("version_drifted_since = ?", time.Now().Add(-73*time.Hour)).
		Where("uid = ?", worker.UID).Exec(t.Context())
	r.NoError(err)

	claimOnNewConnection(t, e, keys, enrolled.AgentUID, "n-2", followingClaim("c2", outdatedOld))

	events := e.agentEvents(models.EventTypeAgentOutdated)
	r.Len(events, 1)
	r.Equal(false, events[0].Payload["legacy"])
	r.Equal(1, e.emailJobs())
}

//nolint:paralleltest // mutates version.Version
func TestPinnedMatchingAndDevAgentsAreNotReported(t *testing.T) {
	cases := []struct {
		name   string
		server string
		frame  func(id, ver string) agentcrypto.ClientFrame
		ver    string
	}{
		{name: "pinned", server: outdatedServer, frame: pinnedClaim, ver: outdatedOld},
		{name: "up to date legacy", server: outdatedServer, frame: legacyClaim, ver: outdatedServer},
		{name: "dev server", server: "dev", frame: legacyClaim, ver: outdatedOld},
		{name: "dev agent", server: outdatedServer, frame: legacyClaim, ver: "dev"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withServerVersion(t, tc.server)

			e := outdatedEnv(t)
			conn, _, _ := e.enroll(e.mintToken(), "paris-dc1")
			resp := roundTrip(t, conn, tc.frame("c1", tc.ver))
			require.Equal(t, agentcrypto.MsgTypeJobs, resp.Type)

			require.Empty(t, e.agentEvents(models.EventTypeAgentOutdated))
			require.Zero(t, e.emailJobs())
		})
	}
}

//nolint:paralleltest // mutates version.Version
func TestSystemAgentIsNeverReported(t *testing.T) {
	withServerVersion(t, outdatedServer)

	r := require.New(t)
	e := newSysEnv(t)

	conn, _, _ := e.enroll(e.mintSystemToken(systemRegion), "fly-machine-1")
	resp := roundTrip(t, conn, legacyClaim("c1", outdatedOld))
	r.Equal(agentcrypto.MsgTypeJobs, resp.Type)

	workers, err := e.dbSvc.ListWorkers(t.Context())
	r.NoError(err)
	r.NotEmpty(workers)

	for _, w := range workers {
		r.Nil(w.OutdatedNotifiedVersion, "a system agent is ours to upgrade")
		r.Nil(w.VersionDriftedSince)
	}
}

// An agent below MinSupportedAgentVersion is told it is unsupported, not just
// outdated: the event carries the flag and the email says so.
//
//nolint:paralleltest // mutates version.Version
func TestUnsupportedAgentNoticeSaysUnsupported(t *testing.T) {
	withServerVersion(t, outdatedServer)

	r := require.New(t)
	e := outdatedEnv(t)

	conn, _, _ := e.enroll(e.mintToken(), "paris-dc1")
	resp := roundTrip(t, conn, legacyClaim("c1", "0.40.0"))
	r.Equal(agentcrypto.MsgTypeJobs, resp.Type)

	events := e.agentEvents(models.EventTypeAgentOutdated)
	r.Len(events, 1)
	r.Equal(true, events[0].Payload["unsupported"])

	var jobs []*models.Job

	r.NoError(e.dbSvc.DB().NewSelect().Model(&jobs).Where("type = ?", "email").Scan(t.Context()))

	found := false

	for _, job := range jobs {
		if job.Config["template"] != "agent-outdated.html" {
			continue
		}

		data, ok := job.Config["templateData"].(map[string]any)
		r.True(ok)
		r.Equal(true, data["Unsupported"])
		r.Equal(agentcrypto.MinSupportedAgentVersion, data["MinVersion"])

		found = true
	}

	r.True(found)
}

func TestOutdatedEmailDataUnsupportedFlag(t *testing.T) {
	t.Parallel()

	data := agentws.OutdatedEmailData("a", "0.40.0", "0.55.0", true, true, "h", "")
	require.Equal(t, true, data["Unsupported"])

	data = agentws.OutdatedEmailData("a", "0.49.3", "0.55.0", true, false, "h", "")
	require.Equal(t, false, data["Unsupported"])
}
