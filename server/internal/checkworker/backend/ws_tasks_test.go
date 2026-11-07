package backend_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/checkworker/tasks"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/agenttasks"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// TestHTTPTaskClientAgainstRealRoutes drives the agent's signed HTTP client
// through the real routes: 204 maps to "no task", a claim round-trips, and a
// finished task answers 410 = ErrTaskGone.
func TestHTTPTaskClientAgainstRealRoutes(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	localfs.Register()

	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "test-secret"
	cfg.FileStorage.Type = "local"
	cfg.FileStorage.LocalRoot = t.TempDir()

	svc := workertasks.NewService(dbSvc.DB())
	svc.SetStateStore(attachments.NewService(files.NewService(dbSvc, cfg), dbSvc, cfg))
	svc.RegisterKind(workertasks.TaskKind{Name: "fake"})

	handler := agenttasks.NewHandler(svc, dbSvc, cfg)
	handler.SetNonceGuard(agents.NewNonceCache())

	router := httpx.New()
	handler.Register(router.NewGroup("/api/v1"))

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	keys, err := agents.GenerateAgentKeys()
	r.NoError(err)

	_, tokenHash, err := agents.GenerateEnrollmentToken()
	r.NoError(err)
	r.NoError(dbSvc.CreateAgentEnrollmentToken(ctx,
		models.NewAgentEnrollmentToken(org.UID, "@dc1", tokenHash, time.Now().Add(time.Hour), nil)))

	agent, err := dbSvc.EnrollAgent(ctx, tokenHash, "agent", keys.Ed25519PublicKey, keys.X25519Recipient,
		agents.KeyFingerprint(keys.Ed25519PublicKey))
	r.NoError(err)

	wsBackend := backend.NewWSBackend(server.URL, "", "agent",
		&backend.Identity{AgentKeys: *keys, AgentUID: agent.UID, Region: "@dc1"}, nil)
	client := wsBackend.TaskClient()

	claim, err := client.Claim(ctx, []string{"fake"})
	r.NoError(err)
	r.Nil(claim)

	check := models.NewCheck(org.UID, "site", "http")
	check.Enabled = false
	r.NoError(dbSvc.CreateCheck(ctx, check))

	task := models.NewWorkerTask(org.UID, "fake", "@dc1")
	task.CheckUID = &check.UID
	r.NoError(svc.Create(ctx, task))

	claim, err = client.Claim(ctx, []string{"fake"})
	r.NoError(err)
	r.NotNil(claim)
	r.Equal(task.UID, claim.UID)

	r.NoError(client.Heartbeat(ctx, task.UID))
	r.NoError(client.Step(ctx, task.UID, workertasks.StepRequest{State: json.RawMessage(`{"n":1}`)}))

	claim, err = client.Claim(ctx, []string{"fake"})
	r.NoError(err)
	r.JSONEq(`{"n":1}`, string(claim.State))

	r.NoError(client.Finish(ctx, task.UID, json.RawMessage(`{}`)))
	r.ErrorIs(client.Heartbeat(ctx, task.UID), workertasks.ErrTaskGone)
	r.ErrorIs(client.Fail(ctx, uuid.NewString(), "x"), workertasks.ErrTaskGone)
}

// An old server has no task routes: its 404 is ErrNotSupported, which the
// loop reads as "nothing to do".
func TestHTTPTaskClientOldServer(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)

	keys, err := agents.GenerateAgentKeys()
	r.NoError(err)

	client := backend.NewWSBackend(server.URL, "", "agent",
		&backend.Identity{AgentKeys: *keys, AgentUID: uuid.NewString()}, nil).TaskClient()

	_, err = client.Claim(t.Context(), []string{"fake"})
	r.ErrorIs(err, tasks.ErrNotSupported)
}

// The identity hello's NoWorkerTasks reaches TasksAllowed on both the
// enrollment and the reconnect path, so a system agent never starts its task
// loop. Without the flag (an org agent, an old server) tasks stay allowed.
func TestHTTPTaskClientTasksAllowed(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		reconnect     bool
		noWorkerTasks bool
	}{
		{name: "enroll, org agent"},
		{name: "enroll, system agent", noWorkerTasks: true},
		{name: "reconnect, org agent", reconnect: true},
		{name: "reconnect, system agent", reconnect: true, noWorkerTasks: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			fake := newFakeAgentServer(t, "eu-west-1")
			fake.noWorkerTasks = tc.noWorkerTasks
			fake.handle(func(_ int, conn *websocket.Conn) { fake.serveReads(conn) })

			keys, err := agents.GenerateAgentKeys()
			r.NoError(err)

			token, identity := "spe_token", &backend.Identity{AgentKeys: *keys}
			if tc.reconnect {
				token, identity.AgentUID = "", "agent-1"
			}

			wsBackend := backend.NewWSBackend(fake.server.URL, token, "agent", identity, nil)

			_, err = wsBackend.Register(t.Context(), nil)
			r.NoError(err)
			r.Equal(!tc.noWorkerTasks, wsBackend.TaskClient().TasksAllowed())
		})
	}
}
