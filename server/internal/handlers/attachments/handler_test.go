package attachments

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

const uploadPath = "/api/v1/agent/attachments"

// nonceCounter makes every generated nonce unique across the package's tests.
//
//nolint:gochecknoglobals // test-only counter
var nonceCounter atomic.Uint64

// signedAgent is an enrolled agent plus the private key needed to sign for it.
type signedAgent struct {
	agent *models.Agent
	priv  ed25519.PrivateKey
}

// enrollAgent creates an active agent row bound to `region`, owned by `orgUID`
// (empty for a system agent), and returns it with its signing key.
func enrollAgent(t *testing.T, ctx context.Context, dbSvc db.Service, orgUID, region string) *signedAgent {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	pubB64 := base64.StdEncoding.EncodeToString(pub)

	// Enroll through the real path (token -> EnrollAgent) rather than
	// inserting a row: the agent identity this endpoint authenticates against
	// is the one enrollment produces, and a hand-built row could drift from it.
	rawToken, tokenHash, err := agents.GenerateEnrollmentToken()
	require.NoError(t, err)
	require.NotEmpty(t, rawToken)

	var token *models.AgentEnrollmentToken
	if orgUID == "" {
		token = models.NewSystemAgentEnrollmentToken(region, tokenHash, time.Now().Add(time.Hour), nil)
	} else {
		token = models.NewAgentEnrollmentToken(orgUID, region, tokenHash, time.Now().Add(time.Hour), nil)
	}

	require.NoError(t, dbSvc.CreateAgentEnrollmentToken(ctx, token))

	agent, err := dbSvc.EnrollAgent(ctx, tokenHash, "agent", pubB64, "age1fake", agents.KeyFingerprint(pubB64))
	require.NoError(t, err)

	return &signedAgent{agent: agent, priv: priv}
}

// signedRequest builds a POST with valid signed headers for this agent.
func (s *signedAgent) signedRequest(t *testing.T, ctx context.Context, topic string, body []byte) *http.Request {
	t.Helper()

	req := httptest.NewRequestWithContext(ctx, http.MethodPost, uploadPath+"?topic="+topic, bytes.NewReader(body))

	timestamp := time.Now().UTC().Format(time.RFC3339)

	// A fresh nonce per request: the endpoint's replay guard is real, and two
	// requests built in the same second would otherwise collide and make an
	// unrelated assertion fail as a 401.
	s.sign(req, timestamp, "nonce-"+strconv.FormatUint(nonceCounter.Add(1), 10))

	return req
}

// sign stamps the four signed headers onto a request.
func (s *signedAgent) sign(req *http.Request, timestamp, nonce string) {
	challenge := agents.SignatureChallenge(req.Method, req.URL.Path, timestamp, nonce)
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(s.priv, challenge))

	req.Header.Set(headerAgentUID, s.agent.UID)
	req.Header.Set(headerTimestamp, timestamp)
	req.Header.Set(headerNonce, nonce)
	req.Header.Set(headerSignature, signature)
}

// uploadFixture is one wired-up endpoint plus the data an upload needs.
type uploadFixture struct {
	ctx      context.Context
	db       db.Service
	svc      *Service
	handler  *Handler
	org      *models.Organization
	incident *models.Incident
	check    *models.Check
}

func setupUploadTest(t *testing.T) *uploadFixture {
	t.Helper()

	ctx, dbService, svc, org := setupAttachmentsTest(t)

	check := models.NewCheck(org.UID, "api", "browser")
	check.Regions = []string{"eu-west"}
	require.NoError(t, dbService.CreateCheck(ctx, check))

	incident := models.NewIncident(org.UID, check.UID, time.Now(), "api is down")
	require.NoError(t, dbService.CreateIncident(ctx, incident))

	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "test-secret"

	handler := NewHandler(svc, dbService, cfg)
	// In-memory replay guard: the sqlite test DB has the agent_nonces table,
	// but an in-process cache keeps the test's intent (replay is refused)
	// independent of the store's schema.
	handler.SetNonceGuard(agents.NewNonceCache())

	return &uploadFixture{
		ctx: ctx, db: dbService, svc: svc, handler: handler,
		org: org, incident: incident, check: check,
	}
}

// serve runs the handler and returns the recorder.
func (f *uploadFixture) serve(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	require.NoError(t, f.handler.Upload(rec, req))

	return rec
}

// TestAgentUploadHappyPath is the positive control every rejection test below
// depends on: a correctly signed agent, serving the incident's region, gets a
// 201 and a real stored attachment.
func TestAgentUploadHappyPath(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setupUploadTest(t)

	agent := enrollAgent(t, f.ctx, f.db, f.org.UID, "eu-west")
	topic := IncidentScreenshotTopic(f.incident.UID)

	rec := f.serve(t, agent.signedRequest(t, f.ctx, topic, pngBytes("agent-capture")))
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var resp UploadResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.NotEmpty(resp.FileUID)

	stored, err := f.db.GetFile(f.ctx, f.org.UID, resp.FileUID)
	r.NoError(err)
	r.NotNil(stored.Topic)
	r.Equal(topic, *stored.Topic)
	r.Equal("image/png", stored.MimeType)
}

// TestAgentUploadRejectsBadSignature covers the authentication surface: missing
// headers, a signature from the wrong key, a stale timestamp, a revoked agent,
// and an unknown agent uid. Each must be a 401 and must store nothing.
func TestAgentUploadRejectsBadSignature(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setupUploadTest(t)

	agent := enrollAgent(t, f.ctx, f.db, f.org.UID, "eu-west")
	topic := IncidentScreenshotTopic(f.incident.UID)

	t.Run("no headers", func(t *testing.T) {
		req := httptest.NewRequestWithContext(f.ctx, http.MethodPost,
			uploadPath+"?topic="+topic, bytes.NewReader(pngBytes("x")))
		r.Equal(http.StatusUnauthorized, f.serve(t, req).Code)
	})

	t.Run("signature from another key", func(t *testing.T) {
		imposter := enrollAgent(t, f.ctx, f.db, f.org.UID, "eu-west")

		req := agent.signedRequest(t, f.ctx, topic, pngBytes("x"))
		// Re-sign with the imposter's key while keeping the victim's agent uid.
		challenge := agents.SignatureChallenge(req.Method, req.URL.Path,
			req.Header.Get(headerTimestamp), req.Header.Get(headerNonce))
		req.Header.Set(headerSignature,
			base64.StdEncoding.EncodeToString(ed25519.Sign(imposter.priv, challenge)))

		r.Equal(http.StatusUnauthorized, f.serve(t, req).Code)
	})

	t.Run("stale timestamp", func(t *testing.T) {
		req := httptest.NewRequestWithContext(f.ctx, http.MethodPost,
			uploadPath+"?topic="+topic, bytes.NewReader(pngBytes("x")))
		stale := time.Now().Add(-2 * agents.DefaultClockSkew).UTC().Format(time.RFC3339)
		agent.sign(req, stale, "stale-nonce")

		r.Equal(http.StatusUnauthorized, f.serve(t, req).Code)
	})

	t.Run("revoked agent", func(t *testing.T) {
		revoked := enrollAgent(t, f.ctx, f.db, f.org.UID, "eu-west")
		r.NoError(f.db.RevokeAgent(f.ctx, f.org.UID, revoked.agent.UID))

		r.Equal(http.StatusUnauthorized,
			f.serve(t, revoked.signedRequest(t, f.ctx, topic, pngBytes("x"))).Code)
	})

	t.Run("unknown agent uid", func(t *testing.T) {
		req := agent.signedRequest(t, f.ctx, topic, pngBytes("x"))
		req.Header.Set(headerAgentUID, "00000000-0000-0000-0000-000000000000")

		r.Equal(http.StatusUnauthorized, f.serve(t, req).Code)
	})
}

// TestAgentUploadRejectsReplay proves the nonce guard: the SAME bytes, headers
// and signature presented twice succeed once and fail once.
func TestAgentUploadRejectsReplay(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setupUploadTest(t)

	agent := enrollAgent(t, f.ctx, f.db, f.org.UID, "eu-west")
	topic := IncidentScreenshotTopic(f.incident.UID)

	timestamp := time.Now().UTC().Format(time.RFC3339)
	const nonce = "replay-me"

	build := func() *http.Request {
		req := httptest.NewRequestWithContext(f.ctx, http.MethodPost,
			uploadPath+"?topic="+topic, bytes.NewReader(pngBytes("replay")))
		agent.sign(req, timestamp, nonce)

		return req
	}

	r.Equal(http.StatusCreated, f.serve(t, build()).Code)
	r.Equal(http.StatusUnauthorized, f.serve(t, build()).Code,
		"a captured signature must not work a second time")
}

// TestAgentUploadRejectsForeignAndMalformedTopics is the authorization surface.
// The org is NEVER taken from the request, so these are the cases where a
// caller tries to take it anyway.
func TestAgentUploadRejectsForeignAndMalformedTopics(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setupUploadTest(t)

	other := models.NewOrganization("acmetwo", "Acme Two")
	r.NoError(f.db.CreateOrganization(f.ctx, other))

	foreignAgent := enrollAgent(t, f.ctx, f.db, other.UID, "eu-west")
	wrongRegionAgent := enrollAgent(t, f.ctx, f.db, f.org.UID, "us-east")
	goodAgent := enrollAgent(t, f.ctx, f.db, f.org.UID, "eu-west")

	topic := IncidentScreenshotTopic(f.incident.UID)

	r.Equal(http.StatusForbidden,
		f.serve(t, foreignAgent.signedRequest(t, f.ctx, topic, pngBytes("x"))).Code,
		"another org's agent must not attach to this incident")

	r.Equal(http.StatusForbidden,
		f.serve(t, wrongRegionAgent.signedRequest(t, f.ctx, topic, pngBytes("x"))).Code,
		"an agent whose region does not serve the check must be refused")

	r.Equal(http.StatusForbidden,
		f.serve(t, goodAgent.signedRequest(t, f.ctx,
			IncidentScreenshotTopic("11111111-1111-1111-1111-111111111111"), pngBytes("x"))).Code,
		"an incident that does not exist must be refused")

	r.Equal(http.StatusForbidden,
		f.serve(t, goodAgent.signedRequest(t, f.ctx,
			"checks/"+f.check.UID+"/screenshot", pngBytes("x"))).Code,
		"an entity with no registered authorizer must fail closed")

	r.Equal(http.StatusBadRequest,
		f.serve(t, goodAgent.signedRequest(t, f.ctx, "incidents..screenshot", pngBytes("x"))).Code,
		"a malformed topic is a bad request")
}

// TestAgentUploadRejectsBadBodies covers the payload gates: a mime mismatch and
// an oversize body, both after a successful authorization so the rejection is
// unambiguously about the body.
func TestAgentUploadRejectsBadBodies(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setupUploadTest(t)

	agent := enrollAgent(t, f.ctx, f.db, f.org.UID, "eu-west")
	topic := IncidentScreenshotTopic(f.incident.UID)

	// A declared image/png that is not one: sniffing is what catches this.
	req := agent.signedRequest(t, f.ctx, topic, []byte("PK\x03\x04 this is a zip"))
	req.Header.Set("Content-Type", "image/png")
	r.Equal(http.StatusUnsupportedMediaType, f.serve(t, req).Code)

	oversize := append(pngBytes(""), make([]byte, MaxAttachmentBytes)...)
	r.Equal(http.StatusRequestEntityTooLarge,
		f.serve(t, agent.signedRequest(t, f.ctx, topic, oversize)).Code)

	r.Equal(http.StatusBadRequest,
		f.serve(t, agent.signedRequest(t, f.ctx, topic, nil)).Code,
		"an empty body is a bad request")
}

// TestAgentUploadRateLimited pins the per-agent cap, and that it is PER AGENT:
// one agent burning its window must not lock another one out.
func TestAgentUploadRateLimited(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	limiter := newAgentRateLimiter(time.Minute, 3)
	now := time.Now()

	r.True(limiter.allow("a", now))
	r.True(limiter.allow("a", now))
	r.True(limiter.allow("a", now))
	r.False(limiter.allow("a", now), "the fourth in the window is refused")

	r.True(limiter.allow("b", now), "the cap is per agent, not global")

	r.True(limiter.allow("a", now.Add(time.Minute)), "the window rolls")
}
