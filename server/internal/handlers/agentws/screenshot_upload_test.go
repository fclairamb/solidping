package agentws_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
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
	// workersSvc is the exact service the WS frame handler submits results
	// through. Reachable here so a test can drive a result whose agent no
	// longer has a socket to send it over.
	workersSvc *workers.Service
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

	return &captureEnv{
		t: t, dbSvc: dbSvc, server: server, org: org,
		attachments: attachmentsSvc, workersSvc: workersSvc,
	}
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

// TestIncidentOpensAfterTheAgentDisconnects is the spec's "agent disconnected
// between the result and the request" case, with a REAL disconnection and an
// observable negative.
//
// Sequencing note, because it decides the shape of this test: on the master the
// incident pipeline runs SYNCHRONOUSLY inside the result submission, so there is
// no instant between "the frame arrived" and "the ask is emitted" at which the
// agent could close its own socket. The production race is therefore reproduced
// from the server's side, which is where it actually happens: the agent's socket
// is torn down, and the result it produced is fed to `workers.SubmitResult` —
// the exact call the WS frame handler makes — because there is no longer a
// socket to carry it.
//
// The disconnected phase alone could only assert absences (no attachment), which
// a socket nobody is listening on would satisfy trivially. So the agent then
// RECONNECTS and a second, known-good ask is provoked: the first upload request
// it receives must be the NEW one. If anything had queued, buffered or retried
// the lost capture, the stale ask would arrive here instead — which is what
// makes "nothing is retried forever" observable rather than assumed, and doubles
// as the control proving the ask machinery works for this very worker.
func TestIncidentOpensAfterTheAgentDisconnects(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	env := newCaptureEnv(t)

	// Both checks exist before the agent connects, so the only frames these
	// connections ever see are the ones the test provokes.
	lost := env.browserCheck("blog")
	later := env.browserCheck("shop")

	conn, keys, workerUID := rawEnroll(t, env)
	jobs := rawClaim(t, conn, lost, later)

	// THE DISCONNECTION, before the result that opens the incident is processed.
	r.NoError(conn.Close(websocket.StatusNormalClosure, "agent going away"))

	r.NoError(env.submitMarkerResult(ctx, jobs[lost.UID], workerUID, "cap-lost"))

	incident, err := env.dbSvc.FindActiveIncidentByCheckUID(ctx, lost.UID)
	r.NoError(err)
	r.NotNil(incident, "a lost capture must never cost us the incident")

	// The agent comes back. Nothing about the lost capture may follow it here.
	revived := env.reconnect(t, keys)

	r.NoError(env.submitMarkerResult(ctx, jobs[later.UID], workerUID, "cap-later"))

	ask := readUploadRequest(t, revived)
	r.Equal("cap-later", ask.CaptureID,
		"a reconnected agent must receive the NEW ask, never a replay of the lost one")

	laterIncident, err := env.dbSvc.FindActiveIncidentByCheckUID(ctx, later.UID)
	r.NoError(err)
	r.Equal(attachments.IncidentScreenshotTopic(laterIncident.UID), ask.Topic)

	// And the incident whose capture was lost never gained one.
	list, err := env.attachments.ListIncidentAttachments(ctx, env.org.UID, incident.UID)
	r.NoError(err)
	r.Empty(list)
}

// TestIncidentOpensWhenTheCaptureIsGone is the neighboring failure the agent
// itself can hit: its connection is perfectly healthy and the upload request IS
// delivered, but the capture named by the marker is no longer in its cache
// (evicted, expired, or the agent restarted). The miss is silent — the incident
// still opens and nothing is uploaded or retried.
func TestIncidentOpensWhenTheCaptureIsGone(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	env := newCaptureEnv(t)
	check := env.browserCheck("shop")
	wsBackend, job, worker := claimJob(t, env, check)

	// A marker naming a capture this agent never held.
	req := downWithCapture("lost")
	req.Diagnostics.Screenshot.PNG = nil
	req.Diagnostics.Screenshot.Available = true
	req.Diagnostics.Screenshot.CaptureID = "capture-nobody-holds"

	r.NoError(wsBackend.SubmitResult(ctx, job, worker.UID, req))

	incident, err := env.dbSvc.FindActiveIncidentByCheckUID(ctx, check.UID)
	r.NoError(err)
	r.NotNil(incident, "a lost capture must never cost us the incident")

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

// rawEnroll performs the enrollment handshake with a bare WebSocket client
// (rather than a WSBackend), so the test owns the socket and can close it at an
// exact point. Returns the connection and the server-assigned worker uid — the
// identity the upload registry is keyed by.
func rawEnroll(t *testing.T, env *captureEnv) (*websocket.Conn, *agentcrypto.AgentKeys, string) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	keys, err := agentcrypto.GenerateAgentKeys()
	r.NoError(err)

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+env.mintToken())

	conn, resp, err := websocket.Dial(ctx, env.server.URL+"/api/v1/agent/ws",
		&websocket.DialOptions{HTTPHeader: headers})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	r.NoError(err)
	t.Cleanup(func() { _ = conn.CloseNow() })

	var hello agentcrypto.ServerFrame
	r.NoError(wsjson.Read(ctx, conn, &hello))
	r.Equal(agentcrypto.MsgTypeHello, hello.Type)

	r.NoError(wsjson.Write(ctx, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeEnroll, ID: "enroll-1", Name: "dc1-agent",
		Ed25519PublicKey: keys.Ed25519PublicKey, X25519PublicKey: keys.X25519Recipient,
	}))

	var enrolled agentcrypto.ServerFrame
	r.NoError(wsjson.Read(ctx, conn, &enrolled))
	r.Equal(agentcrypto.MsgTypeEnrolled, enrolled.Type)
	r.NotEmpty(enrolled.WorkerUID)

	// The identity hello that follows enrollment.
	var identityHello agentcrypto.ServerFrame
	r.NoError(wsjson.Read(ctx, conn, &identityHello))
	r.Equal(agentcrypto.MsgTypeHello, identityHello.Type)

	return conn, keys, enrolled.WorkerUID
}

// reconnect re-dials as the SAME agent with signed headers, the way an agent
// that dropped comes back. The returned connection is the one the registry must
// now be pointing at.
func (e *captureEnv) reconnect(t *testing.T, keys *agentcrypto.AgentKeys) *websocket.Conn {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	agent := e.onlyAgent(t)
	timestamp := time.Now().UTC().Format(time.RFC3339)
	nonce := "reconnect-nonce-1"

	signature, err := keys.Sign(http.MethodGet, "/api/v1/agent/ws", timestamp, nonce)
	r.NoError(err)

	headers := http.Header{}
	headers.Set("X-Sp-Agent-Uid", agent.UID)
	headers.Set("X-Sp-Timestamp", timestamp)
	headers.Set("X-Sp-Nonce", nonce)
	headers.Set("X-Sp-Signature", signature)

	conn, resp, err := websocket.Dial(ctx, e.server.URL+"/api/v1/agent/ws",
		&websocket.DialOptions{HTTPHeader: headers})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	r.NoError(err)
	t.Cleanup(func() { _ = conn.CloseNow() })

	var hello agentcrypto.ServerFrame
	r.NoError(wsjson.Read(ctx, conn, &hello))
	r.Equal(agentcrypto.MsgTypeHello, hello.Type)

	return conn
}

// onlyAgent returns the single agent enrolled in this environment.
func (e *captureEnv) onlyAgent(t *testing.T) *models.Agent {
	t.Helper()

	agents, err := e.dbSvc.ListAgents(t.Context(), e.org.UID)
	require.NoError(t, err)
	require.Len(t, agents, 1)

	return agents[0]
}

// rawClaim claims over a bare connection and returns the wanted checks' jobs,
// keyed by check uid.
func rawClaim(t *testing.T, conn *websocket.Conn, checks ...*models.Check) map[string]string {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	r.NoError(wsjson.Write(ctx, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "claim-1", MaxJobs: 10,
	}))

	var frame agentcrypto.ServerFrame
	r.NoError(wsjson.Read(ctx, conn, &frame))
	r.Equal(agentcrypto.MsgTypeJobs, frame.Type)

	out := map[string]string{}

	for _, job := range frame.Jobs {
		out[job.CheckUID] = job.UID
	}

	for _, check := range checks {
		r.Contains(out, check.UID, "the agent must have claimed every check's job")
	}

	return out
}

// submitMarkerResult feeds a failing, capture-advertising result through the
// EXACT service call the WS frame handler makes. Using it directly is what lets
// a test submit a result whose agent no longer has a socket.
func (e *captureEnv) submitMarkerResult(
	ctx context.Context, jobUID, workerUID, captureID string,
) error {
	_, err := e.workersSvc.SubmitResult(ctx, &workers.SubmitResultRequest{
		JobUID:    jobUID,
		WorkerUID: workerUID,
		Status:    int(models.ResultStatusDown),
		Duration:  42,
		Output:    map[string]any{checkerdef.OutputKeyError: "keyword not found"},
		Diagnostics: &checkerdef.Diagnostics{
			Screenshot: &checkerdef.Screenshot{
				Available: true, CaptureID: captureID, CapturedAt: time.Now().UTC(),
			},
		},
		FromProbe: true,
	})

	return err
}

// readUploadRequest reads frames off a bare connection until an upload request
// arrives, skipping the unrelated hints the server may interleave.
func readUploadRequest(t *testing.T, conn *websocket.Conn) agentcrypto.ServerFrame {
	t.Helper()

	r := require.New(t)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	for {
		var frame agentcrypto.ServerFrame

		r.NoError(wsjson.Read(ctx, conn, &frame), "expected an upload-request frame")

		if frame.Type == agentcrypto.MsgTypeUploadRequest {
			return frame
		}
	}
}
