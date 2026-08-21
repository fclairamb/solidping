package agentws_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/agentws"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/handlers/workers"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// captureEnv is the full capture round-trip environment: a real sqlite DB, the
// real incident pipeline wired to the real attachment rail, and BOTH agent
// routes (the WS control channel and the attachment upload endpoint) on one
// origin — which is what lets a real WSBackend derive the upload URL from its
// server URL, exactly as a deployed agent does.
type captureEnv struct {
	t           *testing.T
	dbSvc       *sqlite.Service
	server      *httptest.Server
	org         *models.Organization
	attachments *attachments.Service
}

func newCaptureEnv(t *testing.T) *captureEnv {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

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

	events := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = events.Close() })

	checkJobSvc := checkjobsvc.NewService(dbSvc.DB())
	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, events, nil)
	attachmentsSvc := attachments.NewService(files.NewService(dbSvc, cfg), dbSvc, cfg)

	incidentsSvc := incidents.NewService(dbSvc, jobs, clock.Real{}, nil)
	incidentsSvc.SetAttachmentStore(attachmentsSvc)

	workersSvc := workers.NewService(dbSvc, checkJobSvc, incidentsSvc, scheduling.Params{})

	handler := agentws.NewHandler(cfg, dbSvc, checkJobSvc, workersSvc, nil, events, nil, nil)

	// The wiring under test: the incident pipeline asks the agent's live
	// connection for the capture it advertised.
	incidentsSvc.SetAgentUploadRequester(handler)

	uploads := attachments.NewHandler(attachmentsSvc, dbSvc, cfg)
	uploads.SetNonceGuard(agentcrypto.NewNonceCache())

	router := httpx.New()
	router.GET("/api/v1/agent/ws", handler.Serve)
	router.POST("/api/v1/agent/attachments", uploads.Upload)

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &captureEnv{t: t, dbSvc: dbSvc, server: server, org: org, attachments: attachmentsSvc}
}

// mintToken creates a live enrollment token bound to (org, testRegion).
func (e *captureEnv) mintToken() string {
	e.t.Helper()

	token, hash, err := agentcrypto.GenerateEnrollmentToken()
	require.NoError(e.t, err)

	row := models.NewAgentEnrollmentToken(e.org.UID, testRegion, hash, time.Now().Add(time.Hour), nil)
	require.NoError(e.t, e.dbSvc.CreateAgentEnrollmentToken(e.t.Context(), row))

	return token
}

// browserCheck creates an enabled browser check pinned to the agent's region,
// with a zero confirmation period so one DOWN result opens an incident.
func (e *captureEnv) browserCheck(slug string) *models.Check {
	e.t.Helper()

	check := models.NewCheck(e.org.UID, slug, "browser")
	check.Config = models.JSONMap{"url": "https://example.com"}
	check.Regions = []string{testRegion}
	check.ConfirmationPeriodSeconds = 0
	require.NoError(e.t, e.dbSvc.CreateCheck(e.t.Context(), check))

	return check
}

// capturedPNG is a real (well, real-enough) PNG: the magic bytes matter because
// the upload endpoint SNIFFS the content type rather than believing it.
func capturedPNG(marker string) []byte {
	return append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte(marker)...)
}

func downWithCapture(marker string) *backend.SubmitResultRequest {
	return &backend.SubmitResultRequest{
		Status:   int(models.ResultStatusDown),
		Duration: 42,
		Output:   map[string]any{checkerdef.OutputKeyError: "keyword not found"},
		Diagnostics: &checkerdef.Diagnostics{
			Screenshot: &checkerdef.Screenshot{
				PNG:        capturedPNG(marker),
				CapturedAt: time.Now().UTC(),
			},
		},
		NextScheduledAt: time.Now().Add(time.Minute),
	}
}

// claimJob enrolls a real WSBackend and claims the check's job.
func claimJob(
	t *testing.T, env *captureEnv, check *models.Check,
) (*backend.WSBackend, *models.CheckJob, *models.Worker) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	keys, err := agentcrypto.GenerateAgentKeys()
	r.NoError(err)

	wsBackend := backend.NewWSBackend(
		env.server.URL, env.mintToken(), "dc1-agent",
		&backend.Identity{AgentKeys: *keys}, nil,
	)

	worker, err := wsBackend.Register(ctx, nil)
	r.NoError(err)

	jobs, _, err := wsBackend.ClaimJobs(ctx, worker.UID, nil, 10, 10, time.Minute)
	r.NoError(err)

	for _, job := range jobs {
		if job.CheckUID == check.UID {
			return wsBackend, job, worker
		}
	}

	r.FailNow("the agent must have claimed the check's job")

	return nil, nil, nil
}

// waitForAttachment polls the incident's attachments until one appears.
func waitForAttachment(
	t *testing.T, env *captureEnv, orgUID, incidentUID string,
) []attachments.Response {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for {
		list, err := env.attachments.ListIncidentAttachments(t.Context(), orgUID, incidentUID)
		require.NoError(t, err)

		if len(list) > 0 {
			return list
		}

		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the agent's capture to be uploaded")
		}

		time.Sleep(20 * time.Millisecond)
	}
}

// TestAgentCaptureReachesTheIncident is the end-to-end the spec asks for, with
// no fakes on any hop: a real agent WSBackend submits a failing result whose
// screenshot bytes stay on the agent; the server opens an incident, pushes an
// upload-request down the very socket the result arrived on; the agent POSTs the
// bytes to the signed attachment endpoint; and the incident ends up with the
// screenshot attached.
func TestAgentCaptureReachesTheIncident(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	env := newCaptureEnv(t)
	check := env.browserCheck("shop")
	wsBackend, job, worker := claimJob(t, env, check)

	r.NoError(wsBackend.SubmitResult(ctx, job, worker.UID, downWithCapture("onset")))

	incident, err := env.dbSvc.FindActiveIncidentByCheckUID(ctx, check.UID)
	r.NoError(err)
	r.NotNil(incident, "the failing result must have opened an incident")

	list := waitForAttachment(t, env, env.org.UID, incident.UID)
	r.Len(list, 1)
	r.Equal(attachments.KindScreenshot, list[0].Kind)
	r.Equal("image/png", list[0].MimeType)
	r.EqualValues(len(capturedPNG("onset")), list[0].Size)

	// The bytes really are the agent's capture, fetched back out of storage.
	stored, err := env.dbSvc.GetFile(ctx, env.org.UID, list[0].UID)
	r.NoError(err)
	r.NotNil(stored.Topic)
	r.Equal(attachments.IncidentScreenshotTopic(incident.UID), *stored.Topic)
}

// TestIncidentOpensWhenTheAgentIsGone is the spec's "agent disconnected between
// the result and the request" case.
//
// The result is submitted over a connection that is then CLOSED before the
// server can ask for the upload — driven directly through the requester with a
// worker uid that has no live connection, which is exactly the state a
// disconnected agent leaves behind. The incident must open regardless, and
// nothing may be retried: no upload ever lands.
func TestIncidentOpensWhenTheAgentIsGone(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	env := newCaptureEnv(t)
	check := env.browserCheck("shop")
	wsBackend, job, worker := claimJob(t, env, check)

	// The agent advertises a capture it will never be asked for, because its
	// socket is gone by the time the request would be sent.
	req := downWithCapture("lost")
	req.Diagnostics.Screenshot.PNG = nil
	req.Diagnostics.Screenshot.Available = true
	req.Diagnostics.Screenshot.CaptureID = "capture-nobody-holds"

	r.NoError(wsBackend.SubmitResult(ctx, job, worker.UID, req))

	incident, err := env.dbSvc.FindActiveIncidentByCheckUID(ctx, check.UID)
	r.NoError(err)
	r.NotNil(incident, "a lost capture must never cost us the incident")

	// Nothing was uploaded, and nothing is pending: there is no retry loop, no
	// queue, and no record that could resurrect this later.
	time.Sleep(300 * time.Millisecond)

	list, err := env.attachments.ListIncidentAttachments(ctx, env.org.UID, incident.UID)
	r.NoError(err)
	r.Empty(list)
}

// TestRequestToAnUnknownWorkerIsANoOp pins the drop path directly: asking for a
// capture from a worker with no live connection must not panic, block, or
// error — it is simply dropped.
func TestRequestToAnUnknownWorkerIsANoOp(t *testing.T) {
	t.Parallel()

	env := newCaptureEnv(t)

	handler := agentws.NewHandler(
		&config.Config{}, env.dbSvc, checkjobsvc.NewService(env.dbSvc.DB()), nil, nil, nil, nil, nil,
	)

	done := make(chan struct{})

	go func() {
		defer close(done)

		handler.RequestScreenshotUpload(context.Background(),
			"worker-that-never-connected", "cap-1", "incidents/inc-1/screenshot")
		// Degenerate inputs must be equally inert.
		handler.RequestScreenshotUpload(context.Background(), "", "cap-1", "incidents/inc-1/screenshot")
		handler.RequestScreenshotUpload(context.Background(), "worker-1", "", "incidents/inc-1/screenshot")
		handler.RequestScreenshotUpload(context.Background(), "worker-1", "cap-1", "")
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RequestScreenshotUpload must never block")
	}
}
