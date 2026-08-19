// Package agentws implements the WebSocket transport for deported check agents
// (spec 2026-07-16-02, generalized to platform-operated agents by
// 2026-07-27-01). It is the ONLY transport an agent has: outbound-only from the
// agent's network, authenticated BEFORE the upgrade (spe_ enrollment bearer on
// first connect; Ed25519 signed headers on every reconnect), and
// surface-limited to exactly claim/result — no config reads, no other entities,
// no org data.
//
// Two agent kinds share every byte of this protocol:
//
//   - org agents (tenant-private): claims are hard-scoped to the agent's org
//     AND exact private region; sealed credential blobs ship VERBATIM and are
//     never decrypted server-side.
//   - system agents (platform-operated, e.g. fly.io machines): claims are
//     scoped to a shared cloud region across every org — the same scope the
//     in-cluster worker claims with. Cloud checks have no sealed envelope, so
//     the server opens the job's server-side envelope and re-seals it to the
//     claiming agent's X25519 key AT CLAIM TIME, producing the very same
//     `configSealed` wire field the agent already knows how to open.
package agentws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/workers"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/version"
)

// Close codes (mirroring the realtimews conventions).
const (
	// CloseAuthFailed signals invalid auth discovered after the upgrade.
	CloseAuthFailed websocket.StatusCode = 4401
	// CloseForbidden signals a revoked agent — terminal until re-enrolled.
	CloseForbidden websocket.StatusCode = 4403
	// CloseProtocolError signals a malformed or unexpected frame.
	CloseProtocolError websocket.StatusCode = 4400
)

const (
	// maxFrameBytes caps one inbound frame (everything an agent sends is
	// untrusted customer input).
	maxFrameBytes = 256 * 1024
	// maxPayloadBytes caps the serialized metrics/output of one result.
	maxPayloadBytes = 64 * 1024
	// maxClaimJobs caps how many jobs one claim frame may take.
	maxClaimJobs = 32
	// enrollTimeout bounds how long the server waits for the enroll frame.
	enrollTimeout = 30 * time.Second
	// pingInterval drives keepalive pings and last_seen_at updates.
	pingInterval = 25 * time.Second
	// claimMaxAhead is the claim-ahead window for agent claims.
	claimMaxAhead = 5 * time.Minute
	// nonceRetention is how long a consumed reconnect nonce stays remembered:
	// twice the accepted clock skew, so a replay is impossible across the whole
	// window a signature can be presented in.
	nonceRetention = 2 * agentcrypto.DefaultClockSkew
)

// signed reconnect headers.
const (
	headerAgentUID  = "X-Sp-Agent-Uid"
	headerTimestamp = "X-Sp-Timestamp"
	headerNonce     = "X-Sp-Nonce"
	headerSignature = "X-Sp-Signature"
)

// ResealFunc re-seals a region's mixed-mode checks after agent membership
// changes (enroll). Optional; nil disables.
type ResealFunc func(ctx context.Context, orgUID, region string)

// Handler serves GET /api/v1/agent/ws.
type Handler struct {
	base.HandlerBase
	dbService    db.Service
	checkJobSvc  checkjobsvc.Service
	workersSvc   *workers.Service
	entitlements *entitlements.Service
	events       notifier.EventNotifier
	// creds opens a cloud job's server-side credential envelope so it can be
	// re-sealed to a claiming system agent (seal-at-claim). Never used on the
	// org-agent path, which stays zero-decrypt.
	creds   credentials.Service
	regions *regions.Service
	// nonces is the reconnect-replay guard. It is the SHARED (database-backed)
	// one in production — see agentcrypto.NonceGuard for why a per-process
	// cache is not sound once the API tier or the agent fleet scales out.
	nonces agentcrypto.NonceGuard
	reseal ResealFunc
	logger *slog.Logger
}

// NewHandler creates the agent WebSocket handler.
func NewHandler(
	cfg *config.Config,
	dbService db.Service,
	checkJobSvc checkjobsvc.Service,
	workersSvc *workers.Service,
	entSvc *entitlements.Service,
	events notifier.EventNotifier,
	creds credentials.Service,
	reseal ResealFunc,
) *Handler {
	return &Handler{
		HandlerBase:  base.NewHandlerBase(cfg),
		dbService:    dbService,
		checkJobSvc:  checkJobSvc,
		workersSvc:   workersSvc,
		entitlements: entSvc,
		events:       events,
		creds:        creds,
		regions:      regions.NewService(dbService),
		nonces:       agentcrypto.NewStoredNonceGuard(dbService, db.ErrAgentNonceReplayed),
		reseal:       reseal,
		logger:       slog.Default().With("component", "agent_ws"),
	}
}

// SetNonceGuard swaps the reconnect-replay guard. It exists for tests and for
// embeddings with no `agent_nonces` table, which can install
// agentcrypto.NewNonceCache() — sound only with a single API replica. Production
// must keep the shared store NewHandler installs.
func (h *Handler) SetNonceGuard(guard agentcrypto.NonceGuard) {
	h.nonces = guard
}

// Serve authenticates (before the upgrade), upgrades, and runs the agent
// connection until it closes.
func (h *Handler) Serve(writer http.ResponseWriter, req *http.Request) error {
	ctx := req.Context()

	authHeader := req.Header.Get("Authorization")

	switch {
	case strings.HasPrefix(authHeader, "Bearer "+agentcrypto.EnrollmentTokenPrefix):
		return h.serveEnrollment(ctx, writer, req)
	case req.Header.Get(headerAgentUID) != "":
		return h.serveReconnect(ctx, writer, req)
	default:
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeNoToken,
			"Provide an enrollment token (Authorization: Bearer spe_…) or signed agent headers")
	}
}

// serveEnrollment handles a first connection carrying a one-shot enrollment
// token. The token is validated (non-consuming) BEFORE the upgrade so a bad
// token gets a real HTTP 401; the atomic consume happens on the enroll frame.
func (h *Handler) serveEnrollment(
	ctx context.Context, writer http.ResponseWriter, req *http.Request,
) error {
	token := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	tokenHash := agentcrypto.HashEnrollmentToken(token)

	if _, err := h.dbService.GetAgentEnrollmentTokenByHash(ctx, tokenHash); err != nil {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeInvalidToken,
			"Enrollment token is invalid, expired, or already used")
	}

	conn, err := websocket.Accept(writer, req, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return nil //nolint:nilerr // the client never got a socket; nothing to answer
	}
	conn.SetReadLimit(maxFrameBytes)

	// Identity is unknown until the enroll frame lands.
	if writeErr := wsjson.Write(ctx, conn, agentcrypto.ServerFrame{
		Type:       agentcrypto.MsgTypeHello,
		Protocol:   agentcrypto.ProtocolVersion,
		ServerTime: time.Now().UTC().Format(time.RFC3339),
	}); writeErr != nil {
		return nil //nolint:nilerr // client already gone; the socket is the response channel
	}

	agent, versionChecked, err := h.awaitEnroll(ctx, conn, tokenHash)
	if err != nil {
		h.logger.WarnContext(ctx, "agent enrollment failed", "error", err)
		_ = conn.Close(CloseAuthFailed, "enrollment failed")

		return nil
	}

	// New recipient in the region: re-seal what the server can (mixed-mode
	// checks) so the fresh agent can decrypt existing credentials. Only org
	// agents have sealed-at-save credentials — a system agent gets its
	// envelopes freshly sealed on every claim instead.
	if h.reseal != nil && !agent.IsSystem() {
		h.reseal(ctx, agent.OrgUID(), agent.Region)
	}

	// versionChecked: true only when the enroll frame actually carried a
	// version to compare — an agent that omitted it (or predates the field)
	// leaves the drift check for the first claim to perform, same as a bare
	// reconnect.
	h.runAgentConnection(ctx, conn, agent, versionChecked)

	return nil
}

// awaitEnroll reads the enroll frame, atomically consumes the token, creates
// the agent, and acknowledges with the agent identity.
//
// The second return, versionChecked, tells the caller whether this call
// already ran the version-drift comparison for real (frame.Version was
// non-empty) — as opposed to having nothing to compare because the agent
// omitted the field. The caller threads it into runAgentConnection's
// connState so a claim landing right after enrollment on the SAME connection
// warns again only if enrollment itself had nothing to check.
func (h *Handler) awaitEnroll(
	ctx context.Context, conn *websocket.Conn, tokenHash string,
) (*models.Agent, bool, error) {
	readCtx, cancel := context.WithTimeout(ctx, enrollTimeout)
	defer cancel()

	var frame agentcrypto.ClientFrame
	if err := wsjson.Read(readCtx, conn, &frame); err != nil {
		return nil, false, fmt.Errorf("read enroll frame: %w", err)
	}

	if frame.Type != agentcrypto.MsgTypeEnroll {
		return nil, false, fmt.Errorf("expected enroll frame, got %q", frame.Type) //nolint:err113 // protocol diagnostic
	}

	if _, err := agentcrypto.ParsePublicKey(frame.Ed25519PublicKey); err != nil {
		return nil, false, err
	}

	if frame.X25519PublicKey == "" || !strings.HasPrefix(frame.X25519PublicKey, "age1") {
		//nolint:err113 // protocol diagnostic
		return nil, false, errors.New("x25519PublicKey must be an age recipient (age1…)")
	}

	token, err := h.dbService.GetAgentEnrollmentTokenByHash(ctx, tokenHash)
	if err != nil {
		return nil, false, fmt.Errorf("lookup enrollment token: %w", err)
	}

	if enrollErr := h.validateEnrollmentScope(ctx, conn, token, frame.ID); enrollErr != nil {
		return nil, false, enrollErr
	}

	name := frame.Name
	if name == "" {
		name = "agent"
	}

	fingerprint := agentcrypto.KeyFingerprint(frame.Ed25519PublicKey)

	agent, err := h.dbService.EnrollAgent(
		ctx, tokenHash, name, frame.Ed25519PublicKey, frame.X25519PublicKey, fingerprint,
	)
	if err != nil {
		return nil, false, err
	}

	// The enroll frame is the agent's FIRST report of its build version — see
	// ws.go's dialEnroll — so it must not sit unused until the first claim
	// lands (claims are throttled at 50s; enroll-only connections would never
	// report at all). Both the write and the drift WARN happen here, once.
	workerUID, err := h.ensureWorkerRow(ctx, agent, frame.Version)
	if err != nil {
		return nil, false, err
	}

	h.warnIfVersionDrift(ctx, agent.UID, frame.Version)

	if err := wsjson.Write(ctx, conn, agentcrypto.ServerFrame{
		Type:        agentcrypto.MsgTypeEnrolled,
		ID:          frame.ID,
		AgentUID:    agent.UID,
		WorkerUID:   workerUID,
		Region:      agent.Region,
		Fingerprint: agent.Fingerprint,
	}); err != nil {
		return nil, false, fmt.Errorf("write enrolled frame: %w", err)
	}

	return agent, frame.Version != "", nil
}

// validateEnrollmentScope runs the per-kind admission checks BEFORE the token is
// consumed, so a rejected enrollment leaves a one-shot org token usable for a
// retry. Each rejection writes an `error` frame naming the cause (the operator
// sees it in agent logs).
//
// A system token is checked against the cloud region catalog exactly like an
// in-cluster worker's SP_REGION (server.go's ValidateWorkerRegion call): a
// platform agent may only serve a defined, non-private region, so a stale or
// mistyped seeded token can never mint an agent bound to nothing — or to
// somebody's private location.
//
// An org token is checked against the MaxDeportedAgents quota. This is not the
// correctness point (MintEnrollmentToken checks it too) but the enrollment-time
// re-check. A nil h.entitlements (disabled in some tests) skips it.
func (h *Handler) validateEnrollmentScope(
	ctx context.Context, conn *websocket.Conn, token *models.AgentEnrollmentToken, frameID string,
) error {
	if token.IsSystem() {
		if regionErr := h.regions.ValidateWorkerRegion(ctx, token.Region); regionErr != nil {
			_ = wsjson.Write(ctx, conn, agentcrypto.ServerFrame{
				Type: agentcrypto.MsgTypeError, ID: frameID,
				Code: string(base.ErrorCodeValidationError), Title: regionErr.Error(),
			})

			return fmt.Errorf("system agent region: %w", regionErr)
		}

		return nil
	}

	if h.entitlements == nil {
		return nil
	}

	quotaErr := h.entitlements.AgentCreateAllowed(ctx, token.OrgUID())
	if quotaErr == nil {
		return nil
	}

	_ = wsjson.Write(ctx, conn, agentcrypto.ServerFrame{
		Type: agentcrypto.MsgTypeError, ID: frameID,
		Code: string(base.ErrorCodeQuotaExceeded), Title: quotaErr.Error(),
	})

	return quotaErr
}

// serveReconnect handles a returning agent authenticating with signed headers:
// Ed25519 over method|path|timestamp|nonce, ±5 min skew, replay-guarded.
func (h *Handler) serveReconnect(
	ctx context.Context, writer http.ResponseWriter, req *http.Request,
) error {
	agentUID := req.Header.Get(headerAgentUID)
	timestamp := req.Header.Get(headerTimestamp)
	nonce := req.Header.Get(headerNonce)
	signature := req.Header.Get(headerSignature)

	if timestamp == "" || nonce == "" || signature == "" {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeNoToken,
			"Missing signed agent headers")
	}

	agent, err := h.dbService.GetAgent(ctx, agentUID)
	if err != nil {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeInvalidToken, "Unknown agent")
	}

	if agent.Status != models.AgentStatusActive {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "Agent has been revoked")
	}

	if tsErr := agentcrypto.ValidateTimestamp(timestamp, time.Now(), agentcrypto.DefaultClockSkew); tsErr != nil {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeInvalidToken,
			"Request timestamp outside the accepted clock skew")
	}

	// Cluster-wide replay guard (spec 2026-07-27-01): the nonce store lives in
	// the database, not in this replica's memory, because a multi-machine fly
	// fleet reconnects through a load balancer and a per-process cache would
	// let a captured signature be replayed against a different replica. Fails
	// CLOSED — a store error rejects the reconnect, which the agent retries.
	if nonceErr := h.nonces.CheckAndStore(
		ctx, agent.UID, nonce, time.Now(), nonceRetention,
	); nonceErr != nil {
		if !errors.Is(nonceErr, agentcrypto.ErrReplayedNonce) {
			h.logger.ErrorContext(ctx, "agent nonce store failed", "error", nonceErr, "agent", agent.UID)
		}

		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeInvalidToken, "Nonce already used")
	}

	if sigErr := agentcrypto.VerifySignature(
		agent.Ed25519PublicKey, req.Method, req.URL.Path, timestamp, nonce, signature,
	); sigErr != nil {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeInvalidToken, "Invalid signature")
	}

	conn, acceptErr := websocket.Accept(writer, req, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if acceptErr != nil {
		return nil //nolint:nilerr // the client never got a socket; nothing to answer
	}
	conn.SetReadLimit(maxFrameBytes)

	// false: a bare reconnect carries no frame at all, so the version-drift
	// check has never had a chance to run yet — the first claim on this
	// connection does it.
	h.runAgentConnection(ctx, conn, agent, false)

	return nil
}

// ensureWorkerRow registers (idempotently, by deterministic slug) the workers
// row that carries the agent's leases and result attribution. Routing never
// goes through the workers row on the agent path — claims are hard-scoped by
// ClaimJobsForAgent — so an org agent's region stays NULL (the workers.region
// check constraint forbids the reserved `@` prefix anyway). A system agent
// serves a real cloud region, so its row records it for fleet observability.
//
// reportedVersion is the agent's self-reported build version, when this
// caller has one to give (the enroll frame carries it; a bare reconnect has
// no frame at all). Empty means "not reported" and leaves any previously
// stored version untouched — same "do not overwrite a known answer with a
// guess" rule UpdateWorkerHeartbeat follows for the claim path.
func (h *Handler) ensureWorkerRow(ctx context.Context, agent *models.Agent, reportedVersion string) (string, error) {
	slug := agentcrypto.WorkerSlug(agent.UID)

	row := &models.Worker{
		UID:  agent.UID, // used only on first insert; RegisterOrUpdateWorker matches by slug afterwards
		Slug: slug,
		Name: "agent:" + agent.Name,
	}

	if agent.IsSystem() {
		region := agent.Region
		row.Region = &region
	}

	if reportedVersion != "" {
		row.Version = &reportedVersion
	}

	worker, err := h.dbService.RegisterOrUpdateWorker(ctx, row)
	if err != nil {
		return "", fmt.Errorf("register agent worker row: %w", err)
	}

	return worker.UID, nil
}

// connState is the per-connection state threaded through the frame handlers.
// It is touched only from the connection's own read loop, so the throttle
// bookkeeping below needs no locking.
type connState struct {
	agent     *models.Agent
	workerUID string
	// egressWrittenAt throttles the workers-row refresh driven by claim frames
	// (see recordAgentEgress). Zero means "never written on this connection".
	egressWrittenAt time.Time
	// versionChecked guards warnOnVersionDrift so it logs AT MOST ONCE per
	// connection, independent of the egress throttle above — a chatty agent
	// must not spam the log once per claim.
	versionChecked bool
}

// agentEgressReportInterval bounds how often a claim frame refreshes the
// agent's workers row. Claims can arrive several times a second on a busy
// agent; the capability they carry changes approximately never, and the row's
// last_active_at only has to stay inside the region liveness window.
const agentEgressReportInterval = 50 * time.Second

// recordAgentEgress refreshes the agent's workers row from a claim frame: its
// last_active_at (which is what region aggregation reads as liveness) and its
// self-reported capability set.
//
// An agent predating the field sends nothing, the decoded slice is nil, and the
// column is left untouched — it stays NULL, i.e. "unknown", which must never be
// rendered as "this location has no IPv6". That is the whole reason the column
// is nullable, and it is what makes the agent/server rollout order irrelevant.
// An agent sending `[]` is a DIFFERENT statement — "I have none" — and is
// written as such.
func (h *Handler) recordAgentEgress(
	ctx context.Context, state *connState, frame *agentcrypto.ClientFrame,
) {
	// Independent of the throttle below: the WARN is about drift detection,
	// not liveness, and must fire once per connection whatever the claim
	// cadence is — a busy agent claiming several times a second must not spam
	// the log once per claim.
	h.warnOnVersionDrift(ctx, state, frame.Version)

	now := time.Now()
	if !state.egressWrittenAt.IsZero() && now.Sub(state.egressWrittenAt) < agentEgressReportInterval {
		return
	}

	state.egressWrittenAt = now

	// The set is untrusted remote input and the column is CHECK-constrained, so
	// a malformed array would fail this UPDATE — and take last_active_at down
	// with it, quietly starving the region of liveness while the agent happily
	// keeps running checks. Drop the bad set instead and still write the
	// heartbeat: nil means "not reported", which leaves whatever is stored
	// exactly as it was.
	capabilities := frame.Capabilities
	if err := models.ValidateCapabilitySet(capabilities); err != nil {
		h.logger.WarnContext(ctx, "agent reported an unstorable capability set; ignoring it",
			"error", err, "agent", state.agent.UID)

		capabilities = nil
	}

	if err := h.dbService.UpdateWorkerHeartbeat(ctx, state.workerUID, capabilities, frame.Version); err != nil {
		h.logger.WarnContext(ctx, "failed to refresh agent worker row",
			"error", err, "agent", state.agent.UID)
	}
}

// warnOnVersionDrift is the claim-path guard: it logs at most once per
// connection (state.versionChecked), independent of the egress throttle,
// then defers to warnIfVersionDrift for the actual comparison. The enroll
// path (awaitEnroll) calls warnIfVersionDrift directly instead — no connState
// exists that early — and tells runAgentConnection it already ran the check,
// so a claim immediately following enrollment on the same connection does
// not warn a second time (see the versionChecked param below).
func (h *Handler) warnOnVersionDrift(ctx context.Context, state *connState, reportedVersion string) {
	if state.versionChecked {
		return
	}

	state.versionChecked = true

	h.warnIfVersionDrift(ctx, state.agent.UID, reportedVersion)
}

// warnIfVersionDrift logs one WARN when the agent's self-reported build
// version differs from this server's own (spec 2026-08-19-07). Detection
// only — nothing here blocks, throttles or disconnects the agent. Callers
// are responsible for the "at most once per connection" guard (see
// warnOnVersionDrift and awaitEnroll); this function itself is a pure
// comparison-and-log with no dedup of its own.
//
// Skipped entirely, on either side, when the version looks like a dev/
// untagged build (version.Version's own zero value, "dev") — otherwise every
// local dev loop and CI run would warn on every agent connection, which
// trains operators to ignore the log line exactly when it would matter.
func (h *Handler) warnIfVersionDrift(ctx context.Context, agentUID, reportedVersion string) {
	if reportedVersion == "" {
		return
	}

	serverVersion := version.Get().Version
	if isDevBuild(serverVersion) || isDevBuild(reportedVersion) {
		return
	}

	if reportedVersion != serverVersion {
		h.logger.WarnContext(ctx, "agent build version differs from this server",
			"agent", agentUID, "agent_version", reportedVersion, "server_version", serverVersion)
	}
}

// isDevBuild reports whether a version string looks like a local/CI build
// rather than a release tag — version.Version defaults to "dev" when ldflags
// were never set, and Get() never changes that default (it only strips a
// leading "v").
func isDevBuild(v string) bool {
	return v == "" || v == "dev"
}

// runAgentConnection sends hello (for reconnects) and serves frames until the
// connection ends.
//
// versionChecked is true when the caller (serveEnrollment, via awaitEnroll)
// already ran the version-drift WARN for this connection off the enroll
// frame; false for a bare reconnect (serveReconnect), which carries no frame
// at all and so has never had the chance. Either way this function's own
// ensureWorkerRow call reports no version ("") — the enroll path already
// wrote it, and a reconnect has nothing new to report — so it can never
// clobber what the enroll frame (or a subsequent claim) already stored.
func (h *Handler) runAgentConnection(
	ctx context.Context, conn *websocket.Conn, agent *models.Agent, versionChecked bool,
) {
	workerUID, err := h.ensureWorkerRow(ctx, agent, "")
	if err != nil {
		h.logger.ErrorContext(ctx, "failed to ensure agent worker row", "error", err)
		_ = conn.Close(websocket.StatusInternalError, "worker registration failed")

		return
	}

	state := &connState{agent: agent, workerUID: workerUID, versionChecked: versionChecked}

	// Reconnects have not seen a hello-with-identity yet; enrollment
	// connections got theirs in the enrolled frame — a second hello echoing
	// the identity is harmless and keeps the protocol uniform.
	if err := wsjson.Write(ctx, conn, agentcrypto.ServerFrame{
		Type:       agentcrypto.MsgTypeHello,
		Protocol:   agentcrypto.ProtocolVersion,
		ServerTime: time.Now().UTC().Format(time.RFC3339),
		AgentUID:   agent.UID,
		WorkerUID:  workerUID,
		Region:     agent.Region,
	}); err != nil {
		return
	}

	_ = h.dbService.UpdateAgentLastSeen(ctx, agent.UID, time.Now())

	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	frames := make(chan agentcrypto.ClientFrame)
	readErr := make(chan error, 1)

	go func() {
		for {
			var frame agentcrypto.ClientFrame
			if err := wsjson.Read(loopCtx, conn, &frame); err != nil {
				readErr <- err

				return
			}

			select {
			case frames <- frame:
			case <-loopCtx.Done():
				return
			}
		}
	}()

	hints := h.events.Listen("check.created")
	defer h.events.Unlisten("check.created", hints)

	ping := time.NewTicker(pingInterval)
	defer ping.Stop()

	for {
		select {
		case <-loopCtx.Done():
			return
		case err := <-readErr:
			h.logger.DebugContext(ctx, "agent connection closed", "agent", agent.UID, "error", err)

			return
		case <-ping.C:
			if !h.handlePingTick(loopCtx, conn, state) {
				return
			}
		case <-hints:
			// Express hint: a check was created somewhere. The agent's claim is
			// hard-scoped, so forwarding unconditionally is safe — a claim for a
			// foreign check simply returns no jobs.
			_ = wsjson.Write(loopCtx, conn, agentcrypto.ServerFrame{Type: agentcrypto.MsgTypeJobsAvailable})
		case frame := <-frames:
			if !h.handleFrame(loopCtx, conn, state, &frame) {
				return
			}
		}
	}
}

// handlePingTick sends a keepalive ping, refreshes last_seen_at, and enforces
// revocation on live connections. Returns false to end the connection.
func (h *Handler) handlePingTick(ctx context.Context, conn *websocket.Conn, state *connState) bool {
	pingCtx, cancel := context.WithTimeout(ctx, pingInterval/2)
	err := conn.Ping(pingCtx)

	cancel()

	if err != nil {
		_ = conn.Close(websocket.StatusGoingAway, "ping timeout")

		return false
	}

	_ = h.dbService.UpdateAgentLastSeen(ctx, state.agent.UID, time.Now())

	// A revoked agent's live connection is closed on the next tick.
	if !h.agentStillActive(ctx, conn, state) {
		return false
	}

	return true
}

// agentStillActive re-fetches the agent and closes the connection when it has
// been revoked (or deleted). Shared by the ping tick and every frame handler.
func (h *Handler) agentStillActive(ctx context.Context, conn *websocket.Conn, state *connState) bool {
	agent, err := h.dbService.GetAgent(ctx, state.agent.UID)
	if err != nil || agent.Status != models.AgentStatusActive {
		_ = conn.Close(CloseForbidden, "agent revoked")

		return false
	}

	return true
}

// handleFrame dispatches one agent frame. Returns false to end the connection.
func (h *Handler) handleFrame(
	ctx context.Context, conn *websocket.Conn, state *connState, frame *agentcrypto.ClientFrame,
) bool {
	if !h.agentStillActive(ctx, conn, state) {
		return false
	}

	switch frame.Type {
	case agentcrypto.MsgTypeClaim:
		h.handleClaim(ctx, conn, state, frame)

		return true
	case agentcrypto.MsgTypeResult:
		h.handleResult(ctx, conn, state, frame)

		return true
	default:
		_ = conn.Close(CloseProtocolError, "unknown frame type")

		return false
	}
}

// agentClaimScope maps an enrolled agent to its claim scope: an org agent is
// pinned to its organization AND its exact private region; a system agent is
// scoped to its shared cloud region alone, across every org. The explicit
// System flag is what makes the widening auditable — checkjobsvc rejects an
// org-less scope that does not set it.
func agentClaimScope(agent *models.Agent) checkjobsvc.AgentScope {
	if agent.IsSystem() {
		return checkjobsvc.AgentScope{Region: agent.Region, System: true}
	}

	return checkjobsvc.AgentScope{OrgUID: agent.OrgUID(), Region: agent.Region}
}

// handleClaim claims jobs hard-scoped by the agent's scope, applies the per-org
// execution rate limit, and dispatches the wire jobs.
func (h *Handler) handleClaim(
	ctx context.Context, conn *websocket.Conn, state *connState, frame *agentcrypto.ClientFrame,
) {
	h.recordAgentEgress(ctx, state, frame)

	maxJobs := frame.MaxJobs
	if maxJobs <= 0 {
		maxJobs = 5
	}

	if maxJobs > maxClaimJobs {
		maxJobs = maxClaimJobs
	}

	jobs, nextIn, err := h.checkJobSvc.ClaimJobsForAgent(
		ctx, state.workerUID, agentClaimScope(state.agent),
		frame.CheckUID, maxJobs, claimMaxAhead,
	)
	if err != nil {
		_ = wsjson.Write(ctx, conn, agentcrypto.ServerFrame{
			Type: agentcrypto.MsgTypeError, ID: frame.ID,
			Code: string(base.ErrorCodeInternalError), Title: "claim failed",
		})

		return
	}

	dispatched := make([]agentcrypto.AgentJob, 0, len(jobs))

	for _, job := range jobs {
		// Per-org MaxChecksPerMinute gate — the agent-side worker loop has no
		// in-process entitlements service, so the cap is enforced here, at
		// dispatch. Drained buckets release the job to its next period.
		if h.entitlements != nil {
			if rateErr := h.entitlements.ReserveCheckExecution(ctx, job.OrganizationUID); rateErr != nil {
				var quotaErr *entitlements.QuotaError
				if errors.As(rateErr, &quotaErr) {
					next := time.Now().Add(time.Duration(job.Period))
					_ = h.checkJobSvc.ReleaseLease(ctx, job.UID, state.workerUID, next)

					continue
				}
			}
		}

		wireJob := agentcrypto.ToAgentJob(job)

		// Seal-at-claim (spec 2026-07-27-01): a cloud job's secrets live in the
		// server-side envelope, which a system agent holds no key for. Open it
		// here and re-seal to this agent's X25519 key so the wire carries the
		// SAME sealed envelope the agent already knows how to open — never a
		// plaintext config. Org agents keep the zero-decrypt path untouched.
		if state.agent.IsSystem() {
			if sealErr := h.sealJobForSystemAgent(ctx, state.agent, job, &wireJob); sealErr != nil {
				h.dropUnsealableJob(ctx, state, job, sealErr)

				continue
			}
		}

		// A tunneled job (`tunnelCheckUid` in its config) needs its SSH check's
		// sealed endpoint attached, snapshotted from the live row at claim time.
		// If the block cannot be built the job is dropped from the batch and an
		// explicit error result is recorded (decision 6) — never dispatched
		// half-armed, never silently skipped.
		if tunnelUID, tunneled := checkerdef.TunnelCheckUIDFrom(job.Config); tunneled {
			tunnel, buildErr := h.buildTunnelBlock(ctx, state.agent, job, tunnelUID)
			if buildErr != nil {
				h.dropTunnelJob(ctx, state, job, buildErr)

				continue
			}

			wireJob.Tunnel = tunnel
		}

		dispatched = append(dispatched, wireJob)
	}

	_ = h.dbService.UpdateAgentLastSeen(ctx, state.agent.UID, time.Now())
	_ = wsjson.Write(ctx, conn, agentcrypto.ServerFrame{
		Type: agentcrypto.MsgTypeJobs, ID: frame.ID, Jobs: dispatched,
		RetryInMs: retryInMs(nextIn),
	})
}

// retryInMs converts the next-eligible hint to wire milliseconds, rounding UP
// so the agent never wakes before the job is actually claimable.
func retryInMs(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}

	return int64((d + time.Millisecond - 1) / time.Millisecond)
}

// buildTunnelBlock loads the referenced SSH check from the live row and RE-ASSERTS
// every tunnel-eligibility rule at CLAIM time — not just at the dependent's
// validation time, since the SSH check's region set, fingerprint, or type could
// have changed since. It ships the SSH check's PUBLIC config plus its
// region-sealed envelope VERBATIM; config_private is never read here (the server
// never decrypts on the agent path). Returns an error naming the fix when the
// block cannot be built, which the caller turns into a dropped job + error
// result.
func (h *Handler) buildTunnelBlock(
	ctx context.Context, agent *models.Agent, job *models.CheckJob, tunnelUID string,
) (*agentcrypto.AgentJobTunnel, error) {
	sshCheck, err := h.dbService.GetCheck(ctx, job.OrganizationUID, tunnelUID)
	if err != nil || sshCheck == nil {
		return nil, fmt.Errorf( //nolint:err113 // dispatch-time diagnostic surfaced to the check's history
			"ssh tunnel check %s no longer exists in this organization", tunnelUID)
	}

	if sshCheck.Type != string(checkerdef.CheckTypeSSH) {
		return nil, fmt.Errorf( //nolint:err113 // dispatch-time diagnostic
			"tunnel check %s is a %q check, only ssh checks can be used as a tunnel", tunnelUID, sshCheck.Type)
	}

	if fingerprint, _ := sshCheck.Config["expected_fingerprint"].(string); fingerprint == "" {
		return nil, fmt.Errorf( //nolint:err113 // dispatch-time diagnostic
			"ssh tunnel check %s must set expected_fingerprint to be used as a tunnel", tunnelUID)
	}

	if _, chained := checkerdef.TunnelCheckUIDFrom(sshCheck.Config); chained {
		return nil, fmt.Errorf( //nolint:err113 // dispatch-time diagnostic
			"ssh tunnel check %s is itself tunneled; chained tunnels are not supported", tunnelUID)
	}

	// Region re-assertion (decision 5): an agent job runs in a private region,
	// and that region MUST be one the SSH check is allocated to — that is what
	// guarantees the SSH check's secrets are already sealed to this agent.
	if job.Region != nil && regions.IsPrivateRegion(*job.Region) && !containsRegion(sshCheck.Regions, *job.Region) {
		return nil, fmt.Errorf( //nolint:err113 // dispatch-time diagnostic
			"ssh tunnel check %s is not allocated to region %s; allocate it there to use it as a tunnel",
			tunnelUID, *job.Region)
	}

	block := &agentcrypto.AgentJobTunnel{
		CheckUID:     sshCheck.UID,
		Config:       map[string]any(sshCheck.Config),
		ConfigSealed: sshCheck.ConfigSealed,
	}

	// A system agent is not a recipient of any sealed-at-save envelope (cloud
	// checks have none), so the bastion's credentials are re-sealed to it here
	// exactly like the job's own — otherwise every tunneled cloud check would
	// fail on a platform region with "not sealed for this agent".
	if agent.IsSystem() {
		sealed, sealErr := h.sealSecretsFor(ctx, agent, sshCheck.OrganizationUID, sshCheck.ConfigPrivate,
			"ssh tunnel check", sshCheck.UID)
		if sealErr != nil {
			return nil, sealErr
		}

		block.ConfigSealed = sealed
	}

	return block, nil
}

// sealJobForSystemAgent replaces the wire job's sealed envelope with one this
// system agent can actually open: the server-side envelope is decrypted here
// (the server holds the master key for cloud checks) and re-sealed to the
// agent's X25519 recipient. A job with no secrets ships with no envelope at all.
//
// The wire field is always overwritten, never merged: whatever `config_sealed`
// the row happened to carry was addressed to some private region's agents and
// is meaningless — and unopenable — here.
func (h *Handler) sealJobForSystemAgent(
	ctx context.Context, agent *models.Agent, job *models.CheckJob, wireJob *agentcrypto.AgentJob,
) error {
	sealed, err := h.sealSecretsFor(ctx, agent, job.OrganizationUID, job.ConfigPrivate, "check", job.CheckUID)
	if err != nil {
		return err
	}

	wireJob.ConfigSealed = sealed

	return nil
}

// sealSecretsFor opens one server-side credential envelope and re-seals it to a
// single agent's X25519 key. It returns nil (no envelope) when there are no
// secrets to ship. Failures return the STATIC actionable reason — the cause,
// which can carry cryptographic detail, is only logged.
func (h *Handler) sealSecretsFor(
	ctx context.Context, agent *models.Agent, orgUID string, envelope *string, kind, uid string,
) (*string, error) {
	secrets, outcome, openErr := checkjobsvc.OpenSecretsEnvelope(ctx, h.creds, orgUID, envelope)

	switch outcome {
	case checkjobsvc.SecretMergeNoop:
		return nil, nil //nolint:nilnil // "no envelope to ship" is genuinely (nil, nil)
	case checkjobsvc.SecretMergeMerged:
		// fall through to sealing
	case checkjobsvc.SecretMergeUnavailable:
		h.logger.ErrorContext(ctx, "cannot seal credentials for system agent: no master key",
			"kind", kind, "uid", uid, "agent", agent.UID)

		return nil, checkjobsvc.ErrSecretsUnavailable
	case checkjobsvc.SecretMergeFailed:
		fallthrough
	default:
		h.logger.ErrorContext(ctx, "cannot seal credentials for system agent",
			"error", openErr, "kind", kind, "uid", uid, "agent", agent.UID)

		return nil, checkjobsvc.ErrSecretsUndecryptable
	}

	sealed, sealErr := credentials.SealForRecipients([]string{agent.X25519PublicKey}, secrets)
	if sealErr != nil {
		h.logger.ErrorContext(ctx, "failed to seal credentials for system agent",
			"error", sealErr, "kind", kind, "uid", uid, "agent", agent.UID)

		return nil, checkjobsvc.ErrSecretsUndecryptable
	}

	return &sealed, nil
}

// dropUnsealableJob applies the established never-dispatch-half-armed contract
// to a job whose credentials could not be sealed for this agent: drop it from
// the batch and record an explicit error result naming the fix (which also
// releases the lease), instead of running the check without its credentials or
// wedging it silently until lease expiry. Mirrors
// DirectBackend.submitSecretsError.
func (h *Handler) dropUnsealableJob(
	ctx context.Context, state *connState, job *models.CheckJob, reason error,
) {
	if _, err := h.workersSvc.SubmitResult(ctx, &workers.SubmitResultRequest{
		JobUID:    job.UID,
		WorkerUID: state.workerUID,
		Status:    int(models.ResultStatusError),
		Output:    map[string]any{checkerdef.OutputKeyError: reason.Error()},
	}); err != nil {
		h.logger.ErrorContext(ctx, "failed to record credential sealing error",
			"error", err, "job_uid", job.UID)
	}
}

// dropTunnelJob records the decision-6 contract for a tunneled job whose tunnel
// block could not be built: an explicit StatusError result naming the fix
// (which also releases the lease via SubmitResult) instead of a half-armed
// dispatch or a silent skip. Mirrors the sealed-envelope drop documented in
// server/CLAUDE.md.
func (h *Handler) dropTunnelJob(ctx context.Context, state *connState, job *models.CheckJob, cause error) {
	h.logger.WarnContext(ctx, "dropping tunneled job: cannot build tunnel block",
		"check_uid", job.CheckUID, "job_uid", job.UID, "error", cause)

	if _, err := h.workersSvc.SubmitResult(ctx, &workers.SubmitResultRequest{
		JobUID:    job.UID,
		WorkerUID: state.workerUID,
		Status:    int(models.ResultStatusError),
		Output: map[string]any{
			checkerdef.OutputKeyError:        "tunnel failed: " + cause.Error(),
			checkerdef.OutputKeyTunnelFailed: true,
			checkerdef.TunnelCheckUIDConfigKey: func() string {
				if uid, ok := checkerdef.TunnelCheckUIDFrom(job.Config); ok {
					return uid
				}

				return ""
			}(),
		},
	}); err != nil {
		h.logger.ErrorContext(ctx, "failed to record tunnel dispatch error", "error", err, "job_uid", job.UID)
	}
}

// containsRegion reports whether want is in the region set.
func containsRegion(regionSet []string, want string) bool {
	for _, region := range regionSet {
		if region == want {
			return true
		}
	}

	return false
}

// handleResult validates and persists one result: lease-ownership guard,
// result row write, and incident processing — identical to the historical
// SubmitResult path, all server-side.
func (h *Handler) handleResult(
	ctx context.Context, conn *websocket.Conn, state *connState, frame *agentcrypto.ClientFrame,
) {
	if reason := validateResultFrame(frame); reason != "" {
		_ = wsjson.Write(ctx, conn, agentcrypto.ServerFrame{
			Type: agentcrypto.MsgTypeError, ID: frame.ID,
			Code: string(base.ErrorCodeValidationError), Title: reason,
		})

		return
	}

	// The job must be inside this agent's scope — an agent can never write
	// results into someone else's. Both kinds require the EXACT region; an org
	// agent additionally requires its own org, while a system agent's shared
	// cloud region legitimately spans every org (the same jobs it may claim).
	job, err := h.dbService.GetCheckJobByUID(ctx, frame.JobUID)
	if err != nil || !agentOwnsJob(state.agent, job) {
		_ = wsjson.Write(ctx, conn, agentcrypto.ServerFrame{
			Type: agentcrypto.MsgTypeError, ID: frame.ID,
			Code: string(base.ErrorCodeForbidden), Title: "job is outside this agent's scope",
		})

		return
	}

	resp, err := h.workersSvc.SubmitResult(ctx, &workers.SubmitResultRequest{
		JobUID:    frame.JobUID,
		WorkerUID: state.workerUID,
		Status:    frame.Status,
		Duration:  frame.Duration,
		Metrics:   frame.Metrics,
		Output:    frame.Output,
		// A result frame is by definition a probe outcome, so the server-side
		// accounting folds its cost/delay samples into the job's EWMAs and lane
		// (spec 2026-07-27-01 item 4). ExecStart is the agent's wall-clock probe
		// start; nil on an agent predating the field, which then contributes no
		// delay sample.
		ExecStart: frame.ExecStart,
		FromProbe: true,
	})
	if err != nil {
		_ = wsjson.Write(ctx, conn, agentcrypto.ServerFrame{
			Type: agentcrypto.MsgTypeError, ID: frame.ID,
			Code: string(base.ErrorCodeInternalError), Title: "result submission failed",
		})

		return
	}

	_ = h.dbService.UpdateAgentLastSeen(ctx, state.agent.UID, time.Now())
	_ = wsjson.Write(ctx, conn, agentcrypto.ServerFrame{
		Type: agentcrypto.MsgTypeAck, ID: frame.ID, NextScheduledAt: &resp.NextScheduledAt,
	})
}

// agentOwnsJob reports whether a job is inside an agent's scope, mirroring
// exactly what agentClaimScope allows it to claim.
func agentOwnsJob(agent *models.Agent, job *models.CheckJob) bool {
	if job == nil || job.Region == nil || *job.Region != agent.Region {
		return false
	}

	if agent.IsSystem() {
		return true
	}

	return job.OrganizationUID == agent.OrgUID()
}

// validateResultFrame applies the untrusted-input checks on a result frame.
// Returns a human-readable rejection reason, or "" when valid.
func validateResultFrame(frame *agentcrypto.ClientFrame) string {
	if frame.JobUID == "" {
		return "jobUid is required"
	}

	if frame.Status < int(models.ResultStatusCreated) || frame.Status > int(models.ResultStatusError) {
		return "status out of range"
	}

	if frame.Duration < 0 {
		return "duration must be non-negative"
	}

	if oversized(frame.Metrics) || oversized(frame.Output) {
		return "metrics/output payload too large"
	}

	return ""
}

// oversized reports whether a payload map serializes past maxPayloadBytes.
func oversized(payload map[string]any) bool {
	if payload == nil {
		return false
	}

	raw, err := json.Marshal(payload)

	return err != nil || len(raw) > maxPayloadBytes
}
