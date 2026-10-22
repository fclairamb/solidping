package agenttasks_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/agentauth"
	"github.com/fclairamb/solidping/server/internal/handlers/agenttasks"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

const testKind = "fake"

var nonceCounter atomic.Uint64 //nolint:gochecknoglobals // unique nonces across tests

type fixture struct {
	db     db.Service
	svc    *workertasks.Service
	router *httpx.Router
	org    *models.Organization
}

func setup(t *testing.T) *fixture {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	localfs.Register()

	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "test-secret"
	cfg.FileStorage.Type = "local"
	cfg.FileStorage.LocalRoot = t.TempDir()

	svc := workertasks.NewService(dbSvc.DB())
	svc.SetStateStore(attachments.NewService(files.NewService(dbSvc, cfg), dbSvc, cfg))
	svc.RegisterKind(workertasks.TaskKind{Name: testKind})

	handler := agenttasks.NewHandler(svc, dbSvc, cfg)
	handler.SetNonceGuard(agents.NewNonceCache())

	router := httpx.New()
	handler.Register(router.NewGroup("/api/v1"))

	return &fixture{db: dbSvc, svc: svc, router: router, org: org}
}

type signedAgent struct {
	agent *models.Agent
	priv  ed25519.PrivateKey
}

func (f *fixture) enroll(t *testing.T, orgUID, region string) *signedAgent {
	t.Helper()

	ctx := t.Context()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	pubB64 := base64.StdEncoding.EncodeToString(pub)

	_, tokenHash, err := agents.GenerateEnrollmentToken()
	require.NoError(t, err)

	var token *models.AgentEnrollmentToken
	if orgUID == "" {
		token = models.NewSystemAgentEnrollmentToken(region, tokenHash, time.Now().Add(time.Hour), nil)
	} else {
		token = models.NewAgentEnrollmentToken(orgUID, region, tokenHash, time.Now().Add(time.Hour), nil)
	}

	require.NoError(t, f.db.CreateAgentEnrollmentToken(ctx, token))

	agent, err := f.db.EnrollAgent(ctx, tokenHash, "agent", pubB64, "age1fake", agents.KeyFingerprint(pubB64))
	require.NoError(t, err)

	return &signedAgent{agent: agent, priv: priv}
}

func (f *fixture) task(t *testing.T) *models.WorkerTask {
	t.Helper()

	check := models.NewCheck(f.org.UID, "c-"+uuid.NewString()[:8], "http")
	check.Enabled = false
	require.NoError(t, f.db.CreateCheck(t.Context(), check))

	task := models.NewWorkerTask(f.org.UID, testKind, "@dc1")
	task.CheckUID = &check.UID
	require.NoError(t, f.svc.Create(t.Context(), task))

	return task
}

// post sends a signed POST (unsigned when agent is nil) and returns the recorder.
func (f *fixture) post(t *testing.T, agent *signedAgent, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, bytes.NewReader(body))

	if agent != nil {
		timestamp := time.Now().UTC().Format(time.RFC3339)
		nonce := "n-" + strconv.FormatUint(nonceCounter.Add(1), 10)
		challenge := agents.SignatureChallenge(req.Method, req.URL.Path, timestamp, nonce)

		req.Header.Set(agentauth.HeaderAgentUID, agent.agent.UID)
		req.Header.Set(agentauth.HeaderTimestamp, timestamp)
		req.Header.Set(agentauth.HeaderNonce, nonce)
		req.Header.Set(agentauth.HeaderSignature, base64.StdEncoding.EncodeToString(ed25519.Sign(agent.priv, challenge)))
	}

	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)

	return rec
}

func claimBody(kinds ...string) []byte {
	return []byte(`{"kinds":["` + strings.Join(kinds, `","`) + `"]}`)
}

// TestAgentTaskRoundTrip is the positive control: a signed org agent claims,
// heartbeats, steps, re-claims with its state, and finishes.
func TestAgentTaskRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setup(t)
	agent := f.enroll(t, f.org.UID, "@dc1")

	rec := f.post(t, agent, "/api/v1/agent/tasks/claim", claimBody(testKind))
	r.Equal(http.StatusNoContent, rec.Code, "nothing to do yet")

	task := f.task(t)

	rec = f.post(t, agent, "/api/v1/agent/tasks/claim", claimBody("unknown-kind", testKind))
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var claim workertasks.Claim
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &claim))
	r.Equal(task.UID, claim.UID)

	base := "/api/v1/agent/tasks/" + task.UID

	rec = f.post(t, agent, base+"/heartbeat", nil)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	rec = f.post(t, agent, base+"/step", []byte(`{"state":{"cursor":7},"progress":{"pages":1}}`))
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	rec = f.post(t, agent, "/api/v1/agent/tasks/claim", claimBody(testKind))
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &claim))
	r.JSONEq(`{"cursor":7}`, string(claim.State))

	rec = f.post(t, agent, base+"/finish", []byte(`{"ok":true}`))
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	// Gone afterwards.
	r.Equal(http.StatusGone, f.post(t, agent, base+"/heartbeat", nil).Code)
	r.Equal(http.StatusGone, f.post(t, agent, base+"/fail", []byte(`{"errorCode":"x"}`)).Code)
	r.Equal(http.StatusGone, f.post(t, agent, "/api/v1/agent/tasks/not-a-uuid/heartbeat", nil).Code)
}

// TestAgentTaskUnknownKindsIgnored: a claim naming only kinds the server does
// not know gets 204 and leaves the task untouched.
func TestAgentTaskUnknownKindsIgnored(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setup(t)
	agent := f.enroll(t, f.org.UID, "@dc1")
	task := f.task(t)

	r.Equal(http.StatusNoContent, f.post(t, agent, "/api/v1/agent/tasks/claim", claimBody("crawl-v9")).Code)

	got, err := f.svc.Get(t.Context(), task.UID)
	r.NoError(err)
	r.Equal(models.WorkerTaskStatusPending, got.Status)
}

func TestAgentTaskAuth(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setup(t)
	f.task(t)

	// Unsigned.
	r.Equal(http.StatusUnauthorized, f.post(t, nil, "/api/v1/agent/tasks/claim", claimBody(testKind)).Code)

	// Revoked.
	revoked := f.enroll(t, f.org.UID, "@dc1")
	r.NoError(f.db.RevokeAgent(t.Context(), f.org.UID, revoked.agent.UID))
	r.Equal(http.StatusUnauthorized, f.post(t, revoked, "/api/v1/agent/tasks/claim", claimBody(testKind)).Code)

	// A system agent is refused: cloud workers cover cloud regions.
	system := f.enroll(t, "", "eu-west")
	r.Equal(http.StatusForbidden, f.post(t, system, "/api/v1/agent/tasks/claim", claimBody(testKind)).Code)
	r.Equal(http.StatusForbidden, f.post(t, system, "/api/v1/agent/tasks/"+uuid.NewString()+"/heartbeat", nil).Code)

	// A wrong key.
	impostor := f.enroll(t, f.org.UID, "@dc1")
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	r.NoError(err)
	impostor.priv = otherPriv
	r.Equal(http.StatusUnauthorized, f.post(t, impostor, "/api/v1/agent/tasks/claim", claimBody(testKind)).Code)
}

func TestAgentTaskBodyCap(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setup(t)
	agent := f.enroll(t, f.org.UID, "@dc1")
	task := f.task(t)

	rec := f.post(t, agent, "/api/v1/agent/tasks/claim", claimBody(testKind))
	r.Equal(http.StatusOK, rec.Code)

	huge := []byte(`{"state":"` + strings.Repeat("a", agenttasks.MaxBodyBytes) + `"}`)
	base := "/api/v1/agent/tasks/" + task.UID

	r.Equal(http.StatusRequestEntityTooLarge, f.post(t, agent, base+"/step", huge).Code)
	r.Equal(http.StatusRequestEntityTooLarge, f.post(t, agent, base+"/finish", huge).Code)
	r.Equal(http.StatusRequestEntityTooLarge, f.post(t, agent, "/api/v1/agent/tasks/claim", huge).Code)

	got, err := f.svc.Get(t.Context(), task.UID)
	r.NoError(err)
	r.Nil(got.StateFileUID, "nothing stored")
	r.Equal(models.WorkerTaskStatusClaimed, got.Status)
}
