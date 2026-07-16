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
	"github.com/uptrace/bunrouter"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/agentws"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/handlers/workers"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// env is the in-process WS test environment: a real sqlite DB, the real
// claim/submit services, and the agentws handler served over httptest.
type env struct {
	t      *testing.T
	dbSvc  *sqlite.Service
	server *httptest.Server
	org    *models.Organization
}

const testRegion = "@acme/dc1"

func newEnv(t *testing.T) *env {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	events := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = events.Close() })

	checkJobSvc := checkjobsvc.NewService(dbSvc.DB())
	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, events, nil)
	creds, err := credentials.NewService(nil, credentials.ParamStore{})
	r.NoError(err)

	workersSvc := workers.NewService(
		dbSvc, checkJobSvc, incidents.NewService(dbSvc, jobs, clock.Real{}, nil), creds,
	)

	handler := agentws.NewHandler(&config.Config{}, dbSvc, checkJobSvc, workersSvc, nil, events, nil)

	router := bunrouter.New()
	router.GET("/api/v1/agent/ws", handler.Serve)

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &env{t: t, dbSvc: dbSvc, server: server, org: org}
}

// mintToken creates a live enrollment token bound to (org, region) and returns
// the plaintext spe_ token.
func (e *env) mintToken(region string) string {
	e.t.Helper()

	token, hash, err := agentcrypto.GenerateEnrollmentToken()
	require.NoError(e.t, err)

	row := models.NewAgentEnrollmentToken(e.org.UID, region, hash, time.Now().Add(time.Hour), nil)
	require.NoError(e.t, e.dbSvc.CreateAgentEnrollmentToken(e.t.Context(), row))

	return token
}

// createCheck creates an enabled check (0s confirmation so a DOWN result opens
// an incident immediately) targeting the given regions; CreateCheck
// materializes one claimable job row per region.
func (e *env) createCheck(slug string, regions []string) *models.Check {
	e.t.Helper()

	check := models.NewCheck(e.org.UID, slug, "http")
	check.Config = models.JSONMap{"url": "https://example.com"}
	check.Regions = regions
	check.ConfirmationPeriodSeconds = 0
	require.NoError(e.t, e.dbSvc.CreateCheck(e.t.Context(), check))

	return check
}

// dial opens a WS connection with the given headers.
func (e *env) dial(headers http.Header) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(e.t.Context(), 10*time.Second)
	e.t.Cleanup(cancel)

	return websocket.Dial(ctx, e.server.URL+"/api/v1/agent/ws", &websocket.DialOptions{
		HTTPHeader: headers,
	})
}

// enroll performs the full enrollment handshake and returns the connection
// (post identity-hello), the agent keys, and the enrolled frame.
func (e *env) enroll(token, name string) (*websocket.Conn, *agentcrypto.AgentKeys, agentcrypto.ServerFrame) {
	e.t.Helper()
	r := require.New(e.t)
	ctx := e.t.Context()

	keys, err := agentcrypto.GenerateAgentKeys()
	r.NoError(err)

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+token)

	conn, _, err := e.dial(headers)
	r.NoError(err)
	e.t.Cleanup(func() { _ = conn.CloseNow() })

	var hello agentcrypto.ServerFrame
	r.NoError(wsjson.Read(ctx, conn, &hello))
	r.Equal(agentcrypto.MsgTypeHello, hello.Type)
	r.Equal(agentcrypto.ProtocolVersion, hello.Protocol)

	r.NoError(wsjson.Write(ctx, conn, agentcrypto.ClientFrame{
		Type:             agentcrypto.MsgTypeEnroll,
		ID:               "enroll-1",
		Name:             name,
		Ed25519PublicKey: keys.Ed25519PublicKey,
		X25519PublicKey:  keys.X25519Recipient,
	}))

	var enrolled agentcrypto.ServerFrame
	r.NoError(wsjson.Read(ctx, conn, &enrolled))
	r.Equal(agentcrypto.MsgTypeEnrolled, enrolled.Type)
	r.NotEmpty(enrolled.AgentUID)
	r.NotEmpty(enrolled.Fingerprint)

	// identity hello
	var identityHello agentcrypto.ServerFrame
	r.NoError(wsjson.Read(ctx, conn, &identityHello))
	r.Equal(agentcrypto.MsgTypeHello, identityHello.Type)
	r.Equal(enrolled.AgentUID, identityHello.AgentUID)

	return conn, keys, enrolled
}

// roundTrip sends a frame and reads frames until one carries the same ID
// (skipping unsolicited jobs-available hints).
func roundTrip(
	t *testing.T, conn *websocket.Conn, frame agentcrypto.ClientFrame,
) agentcrypto.ServerFrame {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	require.NoError(t, wsjson.Write(ctx, conn, frame))

	for {
		var resp agentcrypto.ServerFrame
		require.NoError(t, wsjson.Read(ctx, conn, &resp))

		if resp.ID == frame.ID {
			return resp
		}
	}
}

// TestEnrollClaimResultIncident is the spec's core end-to-end flow: enroll →
// claim (hard-scoped) → submit a result → incident opens on a DOWN result.
func TestEnrollClaimResultIncident(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)
	ctx := t.Context()

	check := e.createCheck("web", []string{testRegion})
	// Foreign scopes the agent must never see: another private region, a cloud
	// region, and a region-less check.
	e.createCheck("other-dc", []string{"@acme/dc2"})
	e.createCheck("cloud", []string{"eu"})
	e.createCheck("bare", nil)

	token := e.mintToken(testRegion)
	conn, _, enrolled := e.enroll(token, "dc1-agent")
	r.Equal(testRegion, enrolled.Region)

	// Claim: exactly the dc1 job — never dc2/cloud/bare (hard scope).
	jobsResp := roundTrip(t, conn, agentcrypto.ClientFrame{Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 10})
	r.Equal(agentcrypto.MsgTypeJobs, jobsResp.Type)
	r.Len(jobsResp.Jobs, 1, "the claim must be hard-scoped to the agent's exact region")
	r.Equal(check.UID, jobsResp.Jobs[0].CheckUID)
	r.NotNil(jobsResp.Jobs[0].Region)
	r.Equal(testRegion, *jobsResp.Jobs[0].Region)

	// Submit a DOWN result: ack + result row + incident (confirmation 0s).
	ack := roundTrip(t, conn, agentcrypto.ClientFrame{
		Type:   agentcrypto.MsgTypeResult,
		ID:     "r1",
		JobUID: jobsResp.Jobs[0].UID,
		Status: int(models.ResultStatusDown),
		Output: map[string]any{"error": "connection refused"},
	})
	r.Equal(agentcrypto.MsgTypeAck, ack.Type)
	r.NotNil(ack.NextScheduledAt)

	results, err := e.dbSvc.GetLastResultForChecks(ctx, e.org.UID, []string{check.UID})
	r.NoError(err)
	got, ok := results[check.UID]
	r.True(ok, "result row must exist")
	r.Equal(int(models.ResultStatusDown), *got.Status)

	incidents, err := e.dbSvc.ListIncidents(ctx, &models.ListIncidentsFilter{OrganizationUID: e.org.UID})
	r.NoError(err)
	r.NotEmpty(incidents, "a DOWN result with 0s confirmation must open an incident server-side")
}

// TestResultOutsideScopeRejected: an agent cannot write results into another
// org/region's job.
func TestResultOutsideScopeRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)
	ctx := t.Context()

	foreign := e.createCheck("foreign", []string{"@acme/dc2"})

	foreignJobs, err := e.dbSvc.ListCheckJobsByCheckUID(ctx, foreign.UID)
	r.NoError(err)
	r.Len(foreignJobs, 1)

	conn, _, _ := e.enroll(e.mintToken(testRegion), "dc1-agent")

	resp := roundTrip(t, conn, agentcrypto.ClientFrame{
		Type:   agentcrypto.MsgTypeResult,
		ID:     "r-foreign",
		JobUID: foreignJobs[0].UID,
		Status: int(models.ResultStatusUp),
	})
	r.Equal(agentcrypto.MsgTypeError, resp.Type)
	r.Equal("FORBIDDEN", resp.Code)
}

// TestEnrollmentTokenSingleUse: the same token cannot enroll twice.
func TestEnrollmentTokenSingleUse(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)

	token := e.mintToken(testRegion)
	_, _, _ = e.enroll(token, "first")

	// Second use fails at the pre-upgrade check with a real HTTP 401.
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+token)
	_, resp, err := e.dial(headers)
	r.Error(err)
	r.NotNil(resp)
	r.Equal(http.StatusUnauthorized, resp.StatusCode)
}

// TestReconnectWithSignedHeaders: a returning agent authenticates with Ed25519
// signed headers; a replayed nonce is rejected; garbage signatures are rejected.
func TestReconnectWithSignedHeaders(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)
	ctx := t.Context()

	e.createCheck("web", []string{testRegion})

	conn, keys, enrolled := e.enroll(e.mintToken(testRegion), "dc1-agent")
	_ = conn.Close(websocket.StatusNormalClosure, "bye")

	signedHeaders := func(nonce string) http.Header {
		timestamp := time.Now().UTC().Format(time.RFC3339)
		signature, signErr := keys.Sign(http.MethodGet, "/api/v1/agent/ws", timestamp, nonce)
		r.NoError(signErr)

		headers := http.Header{}
		headers.Set("X-SP-Agent-Uid", enrolled.AgentUID)
		headers.Set("X-SP-Timestamp", timestamp)
		headers.Set("X-SP-Nonce", nonce)
		headers.Set("X-SP-Signature", signature)

		return headers
	}

	// Fresh reconnect works, and claiming still does too.
	conn2, _, err := e.dial(signedHeaders("nonce-1"))
	r.NoError(err)
	t.Cleanup(func() { _ = conn2.CloseNow() })

	var hello agentcrypto.ServerFrame
	r.NoError(wsjson.Read(ctx, conn2, &hello))
	r.Equal(agentcrypto.MsgTypeHello, hello.Type)
	r.Equal(enrolled.AgentUID, hello.AgentUID)

	jobsResp := roundTrip(t, conn2, agentcrypto.ClientFrame{Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 5})
	r.Equal(agentcrypto.MsgTypeJobs, jobsResp.Type)
	r.Len(jobsResp.Jobs, 1)

	// Replayed nonce -> 401.
	_, resp, err := e.dial(signedHeaders("nonce-1"))
	r.Error(err)
	r.NotNil(resp)
	r.Equal(http.StatusUnauthorized, resp.StatusCode)

	// Wrong key -> 401.
	otherKeys, err := agentcrypto.GenerateAgentKeys()
	r.NoError(err)
	timestamp := time.Now().UTC().Format(time.RFC3339)
	badSig, err := otherKeys.Sign(http.MethodGet, "/api/v1/agent/ws", timestamp, "nonce-2")
	r.NoError(err)

	badHeaders := http.Header{}
	badHeaders.Set("X-SP-Agent-Uid", enrolled.AgentUID)
	badHeaders.Set("X-SP-Timestamp", timestamp)
	badHeaders.Set("X-SP-Nonce", "nonce-2")
	badHeaders.Set("X-SP-Signature", badSig)
	_, resp, err = e.dial(badHeaders)
	r.Error(err)
	r.NotNil(resp)
	r.Equal(http.StatusUnauthorized, resp.StatusCode)

	// Stale timestamp -> 401.
	stale := time.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339)
	staleSig, err := keys.Sign(http.MethodGet, "/api/v1/agent/ws", stale, "nonce-3")
	r.NoError(err)

	staleHeaders := http.Header{}
	staleHeaders.Set("X-SP-Agent-Uid", enrolled.AgentUID)
	staleHeaders.Set("X-SP-Timestamp", stale)
	staleHeaders.Set("X-SP-Nonce", "nonce-3")
	staleHeaders.Set("X-SP-Signature", staleSig)
	_, resp, err = e.dial(staleHeaders)
	r.Error(err)
	r.NotNil(resp)
	r.Equal(http.StatusUnauthorized, resp.StatusCode)
}

// TestRevokedAgentRejected: a revoked agent gets 403 on reconnect and its live
// connection is closed on its next frame.
func TestRevokedAgentRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)
	ctx := t.Context()

	conn, keys, enrolled := e.enroll(e.mintToken(testRegion), "dc1-agent")

	r.NoError(e.dbSvc.RevokeAgent(ctx, e.org.UID, enrolled.AgentUID))

	// Live connection: the next frame closes it with 4403.
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = wsjson.Write(writeCtx, conn, agentcrypto.ClientFrame{Type: agentcrypto.MsgTypeClaim, ID: "c1"})

	var resp agentcrypto.ServerFrame
	readErr := wsjson.Read(writeCtx, conn, &resp)
	r.Error(readErr, "the revoked agent's connection must be closed, not answered")

	// Reconnect: proper HTTP 403 before the upgrade.
	timestamp := time.Now().UTC().Format(time.RFC3339)
	signature, err := keys.Sign(http.MethodGet, "/api/v1/agent/ws", timestamp, "nonce-r")
	r.NoError(err)

	headers := http.Header{}
	headers.Set("X-SP-Agent-Uid", enrolled.AgentUID)
	headers.Set("X-SP-Timestamp", timestamp)
	headers.Set("X-SP-Nonce", "nonce-r")
	headers.Set("X-SP-Signature", signature)
	_, httpResp, err := e.dial(headers)
	r.Error(err)
	r.NotNil(httpResp)
	r.Equal(http.StatusForbidden, httpResp.StatusCode)
}

// TestSecondAgentHA: two agents can enroll into the same region (HA) and each
// can claim work as it becomes due.
func TestSecondAgentHA(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)
	ctx := t.Context()

	checkA := e.createCheck("web-a", []string{testRegion})
	checkB := e.createCheck("web-b", []string{testRegion})

	conn1, _, _ := e.enroll(e.mintToken(testRegion), "agent-1")
	conn2, _, _ := e.enroll(e.mintToken(testRegion), "agent-2")

	active, err := e.dbSvc.ListActiveAgentsByRegion(ctx, e.org.UID, testRegion)
	r.NoError(err)
	r.Len(active, 2, "both agents active in the region")

	// Agent 1 claims one job; agent 2 gets the remaining one — leases keep
	// them from double-claiming.
	jobs1 := roundTrip(t, conn1, agentcrypto.ClientFrame{Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 1})
	r.Len(jobs1.Jobs, 1)

	jobs2 := roundTrip(t, conn2, agentcrypto.ClientFrame{Type: agentcrypto.MsgTypeClaim, ID: "c2", MaxJobs: 5})
	r.Len(jobs2.Jobs, 1)
	r.NotEqual(jobs1.Jobs[0].UID, jobs2.Jobs[0].UID, "the same job must never be claimed twice")

	claimed := map[string]bool{jobs1.Jobs[0].CheckUID: true, jobs2.Jobs[0].CheckUID: true}
	r.True(claimed[checkA.UID] && claimed[checkB.UID], "both checks dispatched exactly once")
}

// TestSealedBlobShipsVerbatim: a private-region job carries the sealed blob
// unmodified, and never the v1 symmetric envelope.
func TestSealedBlobShipsVerbatim(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)
	ctx := t.Context()

	// Enroll first so the check can be sealed to the agent's key.
	conn, keys, _ := e.enroll(e.mintToken(testRegion), "dc1-agent")

	sealed, err := credentials.SealForRecipients(
		[]string{keys.X25519Recipient}, map[string]any{"password": "hunter2"},
	)
	r.NoError(err)

	check := models.NewCheck(e.org.UID, "sealed-check", "http")
	check.Config = models.JSONMap{"url": "https://example.com"}
	check.Regions = []string{testRegion}
	check.ConfigSealed = &sealed
	r.NoError(e.dbSvc.CreateCheck(ctx, check))

	jobsResp := roundTrip(t, conn, agentcrypto.ClientFrame{Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 5})
	r.Len(jobsResp.Jobs, 1)
	r.NotNil(jobsResp.Jobs[0].ConfigSealed)
	r.Equal(sealed, *jobsResp.Jobs[0].ConfigSealed, "the sealed blob must ship verbatim")
	// The public config must not contain the secret.
	_, hasSecret := jobsResp.Jobs[0].Config["password"]
	r.False(hasSecret, "secrets must never appear in the public config on the wire")

	// The agent can unseal it with its identity.
	secrets, err := credentials.UnsealWithIdentity(keys.X25519Identity, *jobsResp.Jobs[0].ConfigSealed)
	r.NoError(err)
	r.Equal("hunter2", secrets["password"])
}
