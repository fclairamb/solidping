// Package agentws implements the WebSocket transport for deported (org-scoped)
// check agents (spec 2026-07-16-02). It is the ONLY transport an agent has:
// outbound-only from the customer network, authenticated BEFORE the upgrade
// (one-shot spe_ enrollment bearer on first connect; Ed25519 signed headers on
// every reconnect), and surface-limited to exactly claim/result — no config
// reads, no other entities, no org data. Claims are hard-scoped to the agent's
// org and exact bound region; sealed credential blobs ship verbatim and are
// never decrypted server-side on this path.
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
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/workers"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/regions"
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
	nonces       *agentcrypto.NonceCache
	reseal       ResealFunc
	logger       *slog.Logger
}

// NewHandler creates the agent WebSocket handler.
func NewHandler(
	cfg *config.Config,
	dbService db.Service,
	checkJobSvc checkjobsvc.Service,
	workersSvc *workers.Service,
	entSvc *entitlements.Service,
	events notifier.EventNotifier,
	reseal ResealFunc,
) *Handler {
	return &Handler{
		HandlerBase:  base.NewHandlerBase(cfg),
		dbService:    dbService,
		checkJobSvc:  checkJobSvc,
		workersSvc:   workersSvc,
		entitlements: entSvc,
		events:       events,
		nonces:       agentcrypto.NewNonceCache(2 * agentcrypto.DefaultClockSkew),
		reseal:       reseal,
		logger:       slog.Default().With("component", "agent_ws"),
	}
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

	agent, err := h.awaitEnroll(ctx, conn, tokenHash)
	if err != nil {
		h.logger.WarnContext(ctx, "agent enrollment failed", "error", err)
		_ = conn.Close(CloseAuthFailed, "enrollment failed")

		return nil
	}

	// New recipient in the region: re-seal what the server can (mixed-mode
	// checks) so the fresh agent can decrypt existing credentials.
	if h.reseal != nil {
		h.reseal(ctx, agent.OrganizationUID, agent.Region)
	}

	h.runAgentConnection(ctx, conn, agent)

	return nil
}

// awaitEnroll reads the enroll frame, atomically consumes the token, creates
// the agent, and acknowledges with the agent identity.
func (h *Handler) awaitEnroll(
	ctx context.Context, conn *websocket.Conn, tokenHash string,
) (*models.Agent, error) {
	readCtx, cancel := context.WithTimeout(ctx, enrollTimeout)
	defer cancel()

	var frame agentcrypto.ClientFrame
	if err := wsjson.Read(readCtx, conn, &frame); err != nil {
		return nil, fmt.Errorf("read enroll frame: %w", err)
	}

	if frame.Type != agentcrypto.MsgTypeEnroll {
		return nil, fmt.Errorf("expected enroll frame, got %q", frame.Type) //nolint:err113 // protocol diagnostic
	}

	if _, err := agentcrypto.ParsePublicKey(frame.Ed25519PublicKey); err != nil {
		return nil, err
	}

	if frame.X25519PublicKey == "" || !strings.HasPrefix(frame.X25519PublicKey, "age1") {
		return nil, errors.New("x25519PublicKey must be an age recipient (age1…)") //nolint:err113 // protocol diagnostic
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
		return nil, err
	}

	workerUID, err := h.ensureWorkerRow(ctx, agent)
	if err != nil {
		return nil, err
	}

	if err := wsjson.Write(ctx, conn, agentcrypto.ServerFrame{
		Type:        agentcrypto.MsgTypeEnrolled,
		ID:          frame.ID,
		AgentUID:    agent.UID,
		WorkerUID:   workerUID,
		Region:      agent.Region,
		Fingerprint: agent.Fingerprint,
	}); err != nil {
		return nil, fmt.Errorf("write enrolled frame: %w", err)
	}

	return agent, nil
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

	if nonceErr := h.nonces.CheckAndStore(agent.UID, nonce); nonceErr != nil {
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

	h.runAgentConnection(ctx, conn, agent)

	return nil
}

// ensureWorkerRow registers (idempotently, by deterministic slug) the workers
// row that carries the agent's leases and result attribution. Its region stays
// NULL — routing never goes through the workers row on the agent path; claims
// are hard-scoped by ClaimJobsForAgent.
func (h *Handler) ensureWorkerRow(ctx context.Context, agent *models.Agent) (string, error) {
	slug := agentWorkerSlug(agent.UID)

	worker, err := h.dbService.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID:  agent.UID, // used only on first insert; RegisterOrUpdateWorker matches by slug afterwards
		Slug: slug,
		Name: "agent:" + agent.Name,
	})
	if err != nil {
		return "", fmt.Errorf("register agent worker row: %w", err)
	}

	return worker.UID, nil
}

// agentWorkerSlug derives the deterministic workers.slug for an agent.
func agentWorkerSlug(agentUID string) string {
	compact := strings.ReplaceAll(agentUID, "-", "")
	if len(compact) > 12 {
		compact = compact[:12]
	}

	return "ag-" + strings.ToLower(compact)
}

// connState is the per-connection state threaded through the frame handlers.
type connState struct {
	agent     *models.Agent
	workerUID string
}

// runAgentConnection sends hello (for reconnects) and serves frames until the
// connection ends.
func (h *Handler) runAgentConnection(ctx context.Context, conn *websocket.Conn, agent *models.Agent) {
	workerUID, err := h.ensureWorkerRow(ctx, agent)
	if err != nil {
		h.logger.ErrorContext(ctx, "failed to ensure agent worker row", "error", err)
		_ = conn.Close(websocket.StatusInternalError, "worker registration failed")

		return
	}

	state := &connState{agent: agent, workerUID: workerUID}

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

// handleClaim claims jobs hard-scoped to the agent's org and exact region,
// applies the per-org execution rate limit, and dispatches the wire jobs.
func (h *Handler) handleClaim(
	ctx context.Context, conn *websocket.Conn, state *connState, frame *agentcrypto.ClientFrame,
) {
	maxJobs := frame.MaxJobs
	if maxJobs <= 0 {
		maxJobs = 5
	}

	if maxJobs > maxClaimJobs {
		maxJobs = maxClaimJobs
	}

	jobs, err := h.checkJobSvc.ClaimJobsForAgent(
		ctx, state.workerUID, state.agent.OrganizationUID, state.agent.Region,
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

		// A tunneled job (`tunnelCheckUid` in its config) needs its SSH check's
		// sealed endpoint attached, snapshotted from the live row at claim time.
		// If the block cannot be built the job is dropped from the batch and an
		// explicit error result is recorded (decision 6) — never dispatched
		// half-armed, never silently skipped.
		if tunnelUID, tunneled := checkerdef.TunnelCheckUIDFrom(job.Config); tunneled {
			tunnel, buildErr := h.buildTunnelBlock(ctx, job, tunnelUID)
			if buildErr != nil {
				h.dropTunnelJob(ctx, state, job, buildErr)

				continue
			}

			wireJob.Tunnel = tunnel
		}

		dispatched = append(dispatched, wireJob)
	}

	_ = h.dbService.UpdateAgentLastSeen(ctx, state.agent.UID, time.Now())
	_ = wsjson.Write(ctx, conn, agentcrypto.ServerFrame{Type: agentcrypto.MsgTypeJobs, ID: frame.ID, Jobs: dispatched})
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
	ctx context.Context, job *models.CheckJob, tunnelUID string,
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

	return &agentcrypto.AgentJobTunnel{
		CheckUID:     sshCheck.UID,
		Config:       map[string]any(sshCheck.Config),
		ConfigSealed: sshCheck.ConfigSealed,
	}, nil
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

	// The job must belong to this agent's org and exact region — an agent can
	// never write results into someone else's scope.
	job, err := h.dbService.GetCheckJobByUID(ctx, frame.JobUID)
	if err != nil || job.OrganizationUID != state.agent.OrganizationUID ||
		job.Region == nil || *job.Region != state.agent.Region {
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
