package agentws_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/agentws"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/handlers/workers"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// systemRegion is the SHARED cloud region a platform agent serves — a plain
// slug, never an `@org/…` private one.
const systemRegion = "eu-west-1"

// sysEnv is the platform-agent test environment: the same handler, but with two
// organizations (to prove the cross-org claim scope), a real credentials
// service (so seal-at-claim has an envelope to open), and a seeded region
// catalog (so system enrollment validates against a real cloud region).
type sysEnv struct {
	*env
	orgB    *models.Organization
	creds   credentials.Service
	events  notifier.EventNotifier
	handler *agentws.Handler
}

// newSysEnv builds the platform-agent environment. It deliberately reuses the
// unexported `env` helpers (dial/enroll/roundTrip) so system agents are proven
// to speak the byte-identical protocol org agents do.
func newSysEnv(t *testing.T) *sysEnv {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	orgA := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, orgA))
	orgB := models.NewOrganization("globex", "Globex")
	r.NoError(dbSvc.CreateOrganization(ctx, orgB))

	r.NoError(dbSvc.SetSystemParameter(ctx, regions.ParamRegions,
		[]regions.RegionDefinition{{Slug: systemRegion, Name: "EU West"}}, false))

	creds := newTestCredentials(t, dbSvc)

	events := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = events.Close() })

	checkJobSvc := checkjobsvc.NewService(dbSvc.DB())
	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, events, nil)
	workersSvc := workers.NewService(
		dbSvc, checkJobSvc, incidents.NewService(dbSvc, jobs, clock.Real{}, nil), scheduling.Params{},
	)

	handler := agentws.NewHandler(&config.Config{}, dbSvc, checkJobSvc, workersSvc, nil, events, creds, nil)

	router := httpx.New()
	router.GET("/api/v1/agent/ws", handler.Serve)

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &sysEnv{
		env:     &env{t: t, dbSvc: dbSvc, server: server, org: orgA},
		orgB:    orgB,
		creds:   creds,
		events:  events,
		handler: handler,
	}
}

// newTestCredentials builds a real (enabled) credentials service over the DB's
// org parameters, exactly like app.BuildCredentialsService does in production.
func newTestCredentials(t *testing.T, dbSvc *sqlite.Service) credentials.Service {
	t.Helper()
	r := require.New(t)

	key := make([]byte, 32)
	_, err := rand.Read(key)
	r.NoError(err)

	store := credentials.ParamStore{
		Load: func(ctx context.Context, orgUID, key string) (json.RawMessage, bool, error) {
			param, getErr := dbSvc.GetOrgParameter(ctx, orgUID, key)
			if getErr != nil || param == nil {
				return nil, false, getErr
			}

			raw, mErr := json.Marshal(param.Value)

			return raw, true, mErr
		},
		Save: func(ctx context.Context, orgUID, key string, value any, secret bool) error {
			return dbSvc.SetOrgParameter(ctx, orgUID, key, value, secret)
		},
	}

	svc, err := credentials.NewService(key, store)
	r.NoError(err)
	r.True(svc.Enabled())

	return svc
}

// mintSystemToken seeds a multi-use platform enrollment token bound to region,
// returning the plaintext spe_ token.
func (e *sysEnv) mintSystemToken(region string) string {
	e.t.Helper()

	token, hash, err := agentcrypto.GenerateEnrollmentToken()
	require.NoError(e.t, err)

	require.NoError(e.t, e.dbSvc.UpsertSystemAgentEnrollmentToken(e.t.Context(),
		models.NewSystemAgentEnrollmentToken(region, hash, time.Now().Add(time.Hour), nil)))

	return token
}

// createCloudCheck creates an enabled check in org, tagged with the shared
// cloud region. When secrets is non-nil they are stored the way the cloud path
// stores them: encrypted server-side into config_private (NOT sealed to any
// agent — that is exactly what seal-at-claim has to solve).
func (e *sysEnv) createCloudCheck(org *models.Organization, slug string, secrets map[string]any) *models.Check {
	e.t.Helper()
	r := require.New(e.t)

	check := models.NewCheck(org.UID, slug, "http")
	check.Config = models.JSONMap{"url": "https://example.com"}
	check.Regions = []string{systemRegion}
	check.ConfirmationPeriodSeconds = 0

	if secrets != nil {
		envelope, err := e.creds.EncryptForOrg(e.t.Context(), org.UID, secrets)
		r.NoError(err)
		check.ConfigPrivate = &envelope

		keys, err := json.Marshal([]string{"password"})
		r.NoError(err)
		keysJSON := string(keys)
		check.ConfigPrivateKeys = &keysJSON
	}

	r.NoError(e.dbSvc.CreateCheck(e.t.Context(), check))

	return check
}

// TestSystemAgentClaimsAcrossOrgs is the central generalization: a platform
// agent bound to a shared cloud region claims that region's jobs for EVERY
// organization — the same scope the in-cluster DirectBackend has — while still
// never seeing another region.
func TestSystemAgentClaimsAcrossOrgs(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newSysEnv(t)

	checkA := e.createCloudCheck(e.org, "acme-web", nil)
	checkB := e.createCloudCheck(e.orgB, "globex-web", nil)

	// Out-of-scope work: another cloud region and a private region.
	other := models.NewCheck(e.org.UID, "other-region", "http")
	other.Config = models.JSONMap{"url": "https://example.com"}
	other.Regions = []string{"us-east-1"}
	r.NoError(e.dbSvc.CreateCheck(t.Context(), other))

	private := models.NewCheck(e.org.UID, "private", "http")
	private.Config = models.JSONMap{"url": "https://example.com"}
	private.Regions = []string{"@dc1"}
	r.NoError(e.dbSvc.CreateCheck(t.Context(), private))

	conn, _, enrolled := e.enroll(e.mintSystemToken(systemRegion), "fly-machine-1")
	r.Equal(systemRegion, enrolled.Region)

	agent, err := e.dbSvc.GetAgent(t.Context(), enrolled.AgentUID)
	r.NoError(err)
	r.True(agent.IsSystem(), "a system token must enroll a system agent")
	r.Nil(agent.OrganizationUID, "a system agent has no owning organization")

	jobsResp := roundTrip(t, conn, agentcrypto.ClientFrame{Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 10})
	r.Equal(agentcrypto.MsgTypeJobs, jobsResp.Type)
	r.Len(jobsResp.Jobs, 2, "a system agent claims its region across every org")

	claimed := map[string]bool{}
	for _, job := range jobsResp.Jobs {
		claimed[job.CheckUID] = true

		r.NotNil(job.Region)
		r.Equal(systemRegion, *job.Region, "the region scope stays exact")
	}

	r.True(claimed[checkA.UID], "org A's job must be claimable")
	r.True(claimed[checkB.UID], "org B's job must be claimable by the same platform agent")
	r.False(claimed[other.UID])
	r.False(claimed[private.UID])
}

// TestOrgAgentStaysOrgScoped is the negative control for the above: the
// widening is attached to the agent's kind, not to the region, so a
// tenant-private agent sharing a region name with nobody still sees only its
// own org's work.
func TestOrgAgentStaysOrgScoped(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newSysEnv(t)

	// Two orgs, both with a job in the SAME private-region slug string.
	mine := models.NewCheck(e.org.UID, "mine", "http")
	mine.Config = models.JSONMap{"url": "https://example.com"}
	mine.Regions = []string{"@dc1"}
	r.NoError(e.dbSvc.CreateCheck(t.Context(), mine))

	theirs := models.NewCheck(e.orgB.UID, "theirs", "http")
	theirs.Config = models.JSONMap{"url": "https://example.com"}
	theirs.Regions = []string{"@dc1"}
	r.NoError(e.dbSvc.CreateCheck(t.Context(), theirs))

	// An ORG token for org A's private region.
	token, hash, err := agentcrypto.GenerateEnrollmentToken()
	r.NoError(err)
	r.NoError(e.dbSvc.CreateAgentEnrollmentToken(t.Context(),
		models.NewAgentEnrollmentToken(e.org.UID, "@dc1", hash, time.Now().Add(time.Hour), nil)))

	conn, _, enrolled := e.enroll(token, "dc1-agent")

	agent, err := e.dbSvc.GetAgent(t.Context(), enrolled.AgentUID)
	r.NoError(err)
	r.False(agent.IsSystem())
	r.Equal(e.org.UID, agent.OrgUID())

	jobsResp := roundTrip(t, conn, agentcrypto.ClientFrame{Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 10})
	r.Len(jobsResp.Jobs, 1, "an org agent never widens across orgs")
	r.Equal(mine.UID, jobsResp.Jobs[0].CheckUID)
}

// TestSystemEnrollmentValidatesRegion: a seeded token whose region is not a
// real cloud region (stale slug, or somebody's private location) can never mint
// an agent — the enrollment is refused before any row is created.
func TestSystemEnrollmentValidatesRegion(t *testing.T) {
	t.Parallel()

	for name, region := range map[string]string{
		"undefined region": "nope-1",
		"private region":   "@dc1",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			e := newSysEnv(t)
			ctx := t.Context()

			headers := http.Header{}
			headers.Set("Authorization", "Bearer "+e.mintSystemToken(region))

			conn, _, err := e.dial(headers)
			r.NoError(err)
			t.Cleanup(func() { _ = conn.CloseNow() })

			var hello agentcrypto.ServerFrame
			r.NoError(wsjson.Read(ctx, conn, &hello))

			keys, err := agentcrypto.GenerateAgentKeys()
			r.NoError(err)
			r.NoError(wsjson.Write(ctx, conn, agentcrypto.ClientFrame{
				Type:             agentcrypto.MsgTypeEnroll,
				ID:               "enroll-1",
				Name:             "fly-machine-1",
				Ed25519PublicKey: keys.Ed25519PublicKey,
				X25519PublicKey:  keys.X25519Recipient,
			}))

			var resp agentcrypto.ServerFrame
			r.NoError(wsjson.Read(ctx, conn, &resp))
			r.Equal(agentcrypto.MsgTypeError, resp.Type, "enrollment must be refused")
		})
	}
}

// TestSystemTokenIsMultiUse: a whole fly fleet enrolls from ONE app-wide
// secret, each machine with its own keypair — while the org token stays
// strictly one-shot.
func TestSystemTokenIsMultiUse(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newSysEnv(t)

	token := e.mintSystemToken(systemRegion)

	_, keys1, first := e.enroll(token, "fly-machine-1")
	_, keys2, second := e.enroll(token, "fly-machine-2")

	r.NotEqual(first.AgentUID, second.AgentUID, "each machine gets its own agent row")
	r.NotEqual(keys1.X25519Recipient, keys2.X25519Recipient,
		"no private key is ever shared between machines")

	// The token records both uses and is still live.
	stored, err := e.dbSvc.GetAgentEnrollmentTokenByHash(t.Context(), agentcrypto.HashEnrollmentToken(token))
	r.NoError(err)
	r.Equal(2, stored.UseCount)
	r.True(stored.HasUsesLeft())
	r.NotNil(stored.UsedAt, "the latest enrollment is recorded")
	r.NotNil(stored.UsedByAgentUID)
	r.Equal(second.AgentUID, *stored.UsedByAgentUID)
}

// TestSystemTokenRespectsMaxUses: an operator can bound a platform token's
// enrollment budget; the (N+1)th machine is refused.
func TestSystemTokenRespectsMaxUses(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newSysEnv(t)
	ctx := t.Context()

	token, hash, err := agentcrypto.GenerateEnrollmentToken()
	r.NoError(err)

	one := 1
	r.NoError(e.dbSvc.UpsertSystemAgentEnrollmentToken(ctx,
		models.NewSystemAgentEnrollmentToken(systemRegion, hash, time.Now().Add(time.Hour), &one)))

	_, _, first := e.enroll(token, "fly-machine-1")
	r.NotEmpty(first.AgentUID)

	// Budget exhausted: the token no longer resolves at all.
	_, err = e.dbSvc.GetAgentEnrollmentTokenByHash(ctx, hash)
	r.Error(err, "a system token past max_uses must stop being usable")

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+token)
	_, status, dialErr := e.dial(headers)
	r.Error(dialErr)
	r.Equal(http.StatusUnauthorized, status)
}

// TestSealAtClaimRoundTrip is the config-delivery guarantee: a cloud check's
// secrets live in a server-side envelope the agent holds no key for, so the
// server re-seals them to the claiming agent's X25519 recipient at claim time.
// The wire carries the same sealed field the agent already knows — and never
// the plaintext secret.
func TestSealAtClaimRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newSysEnv(t)

	check := e.createCloudCheck(e.org, "secret-web", map[string]any{"password": "hunter2"})

	conn, keys, _ := e.enroll(e.mintSystemToken(systemRegion), "fly-machine-1")

	jobsResp := roundTrip(t, conn, agentcrypto.ClientFrame{Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 5})
	r.Len(jobsResp.Jobs, 1)

	job := jobsResp.Jobs[0]
	r.Equal(check.UID, job.CheckUID)
	r.NotNil(job.ConfigSealed, "a cloud job's secrets must be re-sealed to the claiming agent")

	// The plaintext never appears on the wire, in either half.
	_, inConfig := job.Config["password"]
	r.False(inConfig, "the public config must not carry the secret")
	r.NotContains(*job.ConfigSealed, "hunter2")

	// And the agent — and ONLY the agent — can open it.
	secrets, err := credentials.UnsealWithIdentity(keys.X25519Identity, *job.ConfigSealed)
	r.NoError(err)
	r.Equal("hunter2", secrets["password"])

	otherKeys, err := agentcrypto.GenerateAgentKeys()
	r.NoError(err)
	_, err = credentials.UnsealWithIdentity(otherKeys.X25519Identity, *job.ConfigSealed)
	r.Error(err, "the envelope is addressed to this agent alone")
}

// TestSystemAgentResultAcceptedAcrossOrgs: the result-scope guard mirrors the
// claim scope — a platform agent may write results for any org's job in its
// region (and the job it just claimed is exactly such a job).
func TestSystemAgentResultAcceptedAcrossOrgs(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newSysEnv(t)
	ctx := t.Context()

	check := e.createCloudCheck(e.orgB, "globex-web", nil)

	conn, _, _ := e.enroll(e.mintSystemToken(systemRegion), "fly-machine-1")

	jobsResp := roundTrip(t, conn, agentcrypto.ClientFrame{Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 5})
	r.Len(jobsResp.Jobs, 1)

	execStart := time.Now()
	ack := roundTrip(t, conn, agentcrypto.ClientFrame{
		Type:      agentcrypto.MsgTypeResult,
		ID:        "r1",
		JobUID:    jobsResp.Jobs[0].UID,
		Status:    int(models.ResultStatusUp),
		Duration:  42,
		ExecStart: &execStart,
	})
	r.Equal(agentcrypto.MsgTypeAck, ack.Type, "a system agent may submit another org's result in its region")

	results, err := e.dbSvc.GetLastResultForChecks(ctx, e.orgB.UID, []string{check.UID})
	r.NoError(err)
	got, ok := results[check.UID]
	r.True(ok)
	r.Equal(int(models.ResultStatusUp), *got.Status)
}

// TestSharedNonceReplayAcrossHandlers is the P7 guarantee: the replay guard is
// the DATABASE, not one replica's memory. A signature spent against one handler
// instance is refused by a SECOND, independent instance over the same DB —
// which is what a load-balanced API tier serving a multi-machine fly fleet is.
func TestSharedNonceReplayAcrossHandlers(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newSysEnv(t)
	ctx := t.Context()

	conn, keys, enrolled := e.enroll(e.mintSystemToken(systemRegion), "fly-machine-1")
	_ = conn.Close(websocket.StatusNormalClosure, "bye")

	// A SECOND API replica over the same database.
	replica := e.newReplica()

	signedHeaders := func(nonce string) http.Header {
		timestamp := time.Now().UTC().Format(time.RFC3339)
		signature, signErr := keys.Sign(http.MethodGet, "/api/v1/agent/ws", timestamp, nonce)
		r.NoError(signErr)

		headers := http.Header{}
		headers.Set("X-Sp-Agent-Uid", enrolled.AgentUID)
		headers.Set("X-Sp-Timestamp", timestamp)
		headers.Set("X-Sp-Nonce", nonce)
		headers.Set("X-Sp-Signature", signature)

		return headers
	}

	// Spend the nonce on replica 1.
	conn2, _, err := e.dial(signedHeaders("nonce-shared"))
	r.NoError(err)
	t.Cleanup(func() { _ = conn2.CloseNow() })

	var hello agentcrypto.ServerFrame
	r.NoError(wsjson.Read(ctx, conn2, &hello))
	r.Equal(agentcrypto.MsgTypeHello, hello.Type)

	// Replay it against replica 2: refused, because the guard is shared.
	_, status, replayErr := dialAt(t, replica.URL, signedHeaders("nonce-shared"))
	r.Error(replayErr)
	r.Equal(http.StatusUnauthorized, status,
		"a nonce spent on one replica must be refused by every other replica")

	// A fresh nonce still works on replica 2 — the guard rejects replays, not
	// the replica itself.
	conn3, _, err := dialAt(t, replica.URL, signedHeaders("nonce-fresh"))
	r.NoError(err)
	t.Cleanup(func() { _ = conn3.CloseNow() })
	r.NoError(wsjson.Read(ctx, conn3, &hello))
	r.Equal(agentcrypto.MsgTypeHello, hello.Type)
}

// newReplica starts a second, independent agentws handler over the SAME
// database — the load-balanced API tier a fly fleet reconnects through.
func (e *sysEnv) newReplica() *httptest.Server {
	e.t.Helper()

	events := notifier.NewLocalEventNotifier()
	e.t.Cleanup(func() { _ = events.Close() })

	checkJobSvc := checkjobsvc.NewService(e.dbSvc.DB())
	jobs := jobsvc.NewService(e.dbSvc.DB(), e.dbSvc, events, nil)
	workersSvc := workers.NewService(
		e.dbSvc, checkJobSvc, incidents.NewService(e.dbSvc, jobs, clock.Real{}, nil), scheduling.Params{},
	)

	handler := agentws.NewHandler(&config.Config{}, e.dbSvc, checkJobSvc, workersSvc, nil, events, e.creds, nil)

	router := httpx.New()
	router.GET("/api/v1/agent/ws", handler.Serve)

	server := httptest.NewServer(router)
	e.t.Cleanup(server.Close)

	return server
}

// dialAt is env.dial against an arbitrary server URL.
func dialAt(t *testing.T, baseURL string, headers http.Header) (*websocket.Conn, int, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	conn, resp, err := websocket.Dial(ctx, baseURL+"/api/v1/agent/ws", &websocket.DialOptions{HTTPHeader: headers})

	status := 0
	if resp != nil {
		status = resp.StatusCode

		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}

	return conn, status, err
}

// TestInMemoryNonceGuardStillWorks keeps the no-database NonceGuard honest: it
// is the documented single-replica implementation, so it must reject a replay
// within the window and accept a distinct (agent, nonce) pair.
func TestInMemoryNonceGuardStillWorks(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cache := agentcrypto.NewNonceCache()
	ctx := t.Context()
	now := time.Now()
	retain := 10 * time.Minute

	r.NoError(cache.CheckAndStore(ctx, "agent-1", "n1", now, retain))
	r.ErrorIs(cache.CheckAndStore(ctx, "agent-1", "n1", now, retain), agentcrypto.ErrReplayedNonce)
	r.NoError(cache.CheckAndStore(ctx, "agent-1", "n2", now, retain), "a different nonce is fine")
	r.NoError(cache.CheckAndStore(ctx, "agent-2", "n1", now, retain), "the guard is keyed per agent")
	r.NoError(cache.CheckAndStore(ctx, "agent-1", "n1", now.Add(2*retain), retain),
		"outside the retention window the pair is forgettable")
	r.Positive(cache.Len())
}

// TestJobsAvailableHintFansOutAcrossOrgs pins P3's "verified rather than
// changed" claim: the express hint rides a GLOBAL `check.created` listen, so a
// system agent is woken by a check created in an org it has no relationship
// with — which is exactly the set of jobs it may now claim. Without this the
// platform region would fall back to poll latency on every new check.
func TestJobsAvailableHintFansOutAcrossOrgs(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newSysEnv(t)
	ctx := t.Context()

	conn, _, _ := e.enroll(e.mintSystemToken(systemRegion), "fly-machine-1")

	// Drain the connection until the hint arrives (or the read times out).
	hints := make(chan agentcrypto.ServerFrame, 4)

	go func() {
		for {
			var frame agentcrypto.ServerFrame
			if err := wsjson.Read(ctx, conn, &frame); err != nil {
				close(hints)

				return
			}

			if frame.Type == agentcrypto.MsgTypeJobsAvailable {
				hints <- frame

				return
			}
		}
	}()

	// A check created in the OTHER organization. The handler listens on the
	// GLOBAL `check.created` topic (the checks service emits it; here the
	// notifier stands in for it), with no org or region filter at all.
	e.createCloudCheck(e.orgB, "globex-web", nil)
	r.NoError(e.events.Notify(ctx, string(models.EventTypeCheckCreated), "{}"))

	select {
	case frame, ok := <-hints:
		r.True(ok, "the connection closed before the hint arrived")
		r.Equal(agentcrypto.MsgTypeJobsAvailable, frame.Type)
	case <-time.After(5 * time.Second):
		r.Fail("a system agent must be hinted about any org's new check in its region")
	}
}

// TestUnsealableCloudJobIsDroppedWithErrorResult applies the established
// never-dispatch-half-armed contract to seal-at-claim: a cloud job whose
// server-side envelope cannot be opened is dropped from the batch AND recorded
// as an explicit error result (which releases the lease), instead of running
// the check without its credentials or wedging it until lease expiry.
func TestUnsealableCloudJobIsDroppedWithErrorResult(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newSysEnv(t)
	ctx := t.Context()

	healthy := e.createCloudCheck(e.org, "healthy", nil)

	// A check whose config_private is not a decryptable envelope at all.
	broken := models.NewCheck(e.org.UID, "broken", "http")
	broken.Config = models.JSONMap{"url": "https://example.com"}
	broken.Regions = []string{systemRegion}
	garbage := `{"v":1,"alg":"aes-256-gcm","ct":"bm90LWFuLWVudmVsb3Bl"}`
	broken.ConfigPrivate = &garbage
	r.NoError(e.dbSvc.CreateCheck(ctx, broken))

	conn, _, _ := e.enroll(e.mintSystemToken(systemRegion), "fly-machine-1")

	jobsResp := roundTrip(t, conn, agentcrypto.ClientFrame{Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 10})
	r.Len(jobsResp.Jobs, 1, "the unsealable job must be dropped from the batch")
	r.Equal(healthy.UID, jobsResp.Jobs[0].CheckUID)

	// The dropped job produced an explicit error result naming the failure.
	results, err := e.dbSvc.GetLastResultForChecks(ctx, e.org.UID, []string{broken.UID})
	r.NoError(err)
	got, ok := results[broken.UID]
	r.True(ok, "the dropped job must record an error result, not vanish")
	r.Equal(int(models.ResultStatusError), *got.Status)
	r.NotEmpty(got.Output)
}
