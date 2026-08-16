package backend

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkssh"
	"github.com/fclairamb/solidping/server/internal/checkworker/egressreport"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Errors returned by WSBackend.
var (
	ErrNotEnrolled        = errors.New("agent is not enrolled yet")
	ErrEnrollNoToken      = errors.New("no enrollment token configured (SP_AGENT_ENROLLMENT_TOKEN)")
	ErrRequestTimeout     = errors.New("agent request timed out")
	ErrServerError        = errors.New("server rejected the request")
	ErrPassiveUnsupported = errors.New("passive checks are not supported on deported agents")
	// ErrConnLost fails an in-flight request the moment its connection is
	// retired, instead of waiting out the full requestTimeout.
	ErrConnLost = errors.New("agent connection lost")
	// ErrSealedForOthers is surfaced as the job error when a sealed blob cannot
	// be decrypted by this agent — the fix is a credentials re-save.
	ErrSealedForOthers = errors.New(
		"credentials not sealed for this agent — re-save the check's credentials")
	// ErrTunnelSealedForOthers is surfaced as the job error when a tunnel block's
	// sealed envelope cannot be decrypted by this agent (spec 2026-07-18-07). The
	// SSH check is not, in fact, sealed to this agent's region.
	ErrTunnelSealedForOthers = errors.New(
		"ssh tunnel credentials not sealed for this agent — " +
			"allocate the SSH check to this agent's region and re-save its credentials")
	// ErrTunnelNoCredentials is surfaced when an unsealed tunnel block has no
	// usable auth material (no username, or no password/private_key).
	ErrTunnelNoCredentials = errors.New(
		"ssh tunnel check needs a username and a password or private_key")
)

const (
	// wsPath is the agent transport endpoint.
	wsPath = "/api/v1/agent/ws"
	// requestTimeout bounds one claim/result round-trip.
	requestTimeout = 30 * time.Second
	// reconnectBackoff is the base reconnect delay and the lazy-dial min-spacing.
	reconnectBackoff = 5 * time.Second
	// maxReconnectBackoff caps the exponential reconnect backoff.
	maxReconnectBackoff = 60 * time.Second
	// defaultPingInterval is the agent-side keepalive cadence (mirrors the
	// server's pingInterval); a failed ping means a half-open link.
	defaultPingInterval = 25 * time.Second
	// codeConnLost is the synthetic error-frame code retire delivers to in-flight
	// waiters so request() can fail fast on a dropped connection.
	codeConnLost = "AGENT_CONN_LOST"
)

// Identity is the persisted agent identity: its two keypairs plus the
// server-assigned UID and bound region learned at enrollment.
type Identity struct {
	agents.AgentKeys
	AgentUID string `json:"agentUid,omitempty"`
	Region   string `json:"region,omitempty"`
}

// WSBackend implements WorkerBackend over the WebSocket agent protocol. It
// owns the connection lifecycle: enrollment on first run, signed-header
// reconnects afterwards, correlation-id request/response, jobs-available hint
// fan-out, and unsealing of region-sealed credentials (in memory only).
type WSBackend struct {
	serverURL       string
	enrollmentToken string
	name            string

	// dialMu serializes dialing so only one connect runs at a time regardless
	// of which path triggers it (lazy request() vs. the reconnect supervisor).
	dialMu sync.Mutex

	mu                sync.Mutex
	identity          *Identity
	conn              *websocket.Conn
	connectedAt       time.Time
	workerUID         string
	pending           map[string]chan agents.ServerFrame
	hints             []chan string
	lastDial          time.Time
	supervisorStarted bool
	// reconnectSignal wakes the reconnect supervisor after a drop (buffered so a
	// drop that races an in-progress reconnect is coalesced, never lost).
	reconnectSignal chan struct{}

	// pingInterval is the keepalive cadence (overridable in tests).
	pingInterval time.Duration

	// onIdentityChange persists the identity after enrollment.
	onIdentityChange func(*Identity)

	// tunnelMu guards the in-memory unsealed SSH-tunnel snapshots. They exist
	// ONLY in memory, keyed by tunnel check UID, lifecycle-bound to the claimed
	// jobs that carry them (registered on claim, released on result submission).
	tunnelMu sync.Mutex
	tunnels  map[string]*tunnelSnapshot

	logger *slog.Logger
}

// tunnelSnapshot is one in-memory, unsealed SSH-tunnel endpoint. refs counts how
// many currently-claimed jobs reference this tunnel check so the plaintext is
// dropped as soon as the last one finishes.
type tunnelSnapshot struct {
	cfg  *checkssh.SSHConfig
	refs int
}

// Option customizes a WSBackend at construction.
type Option func(*WSBackend)

// WithLogger overrides the default logger (used in tests to capture output).
func WithLogger(logger *slog.Logger) Option {
	return func(b *WSBackend) { b.logger = logger }
}

// WithPingInterval overrides the keepalive ping cadence (used in tests to force
// fast half-open detection).
func WithPingInterval(interval time.Duration) Option {
	return func(b *WSBackend) { b.pingInterval = interval }
}

// NewWSBackend creates a WSBackend. identity must carry the agent's keypairs;
// AgentUID/Region may be empty (enrollment fills them, then onIdentityChange
// fires so the caller can persist).
func NewWSBackend(
	serverURL, enrollmentToken, name string,
	identity *Identity,
	onIdentityChange func(*Identity),
	opts ...Option,
) *WSBackend {
	backend := &WSBackend{
		serverURL:        serverURL,
		enrollmentToken:  enrollmentToken,
		name:             name,
		identity:         identity,
		pending:          map[string]chan agents.ServerFrame{},
		reconnectSignal:  make(chan struct{}, 1),
		pingInterval:     defaultPingInterval,
		onIdentityChange: onIdentityChange,
		tunnels:          map[string]*tunnelSnapshot{},
		logger:           slog.Default().With("component", "agent_ws_backend"),
	}

	for _, opt := range opts {
		opt(backend)
	}

	return backend
}

// Identity returns a copy of the current identity (for persistence/logging).
func (b *WSBackend) Identity() Identity {
	b.mu.Lock()
	defer b.mu.Unlock()

	return *b.identity
}

// Register satisfies WorkerBackend. The server registers the worker row during
// the WS handshake; this just ensures a connection exists and returns the
// server-assigned identity.
func (b *WSBackend) Register(ctx context.Context, _ *models.Worker) (*models.Worker, error) {
	if err := b.ensureConn(ctx); err != nil {
		return nil, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	region := b.identity.Region

	return &models.Worker{
		UID:    b.workerUID,
		Slug:   "agent",
		Name:   b.name,
		Region: &region,
	}, nil
}

// Heartbeat is a no-op: the server refreshes last_seen_at on pings and frames,
// and the agent's egress capability rides the claim frame (see claim) for the
// same reason.
func (b *WSBackend) Heartbeat(_ context.Context, _ string, _ models.WorkerEgress) error {
	return nil
}

// ClaimJobs claims jobs over the WS transport (single limit — the lane
// reservation is an in-process pool concern; the server orders cost-aware).
// Sealed credentials are unsealed here with the agent's X25519 identity and
// merged into the job config, in memory only; a blob this agent cannot decrypt
// is reported as a clear job error and dropped from the batch.
func (b *WSBackend) ClaimJobs(
	ctx context.Context,
	_ string,
	_ *string,
	fastLimit int,
	_ int,
	_ time.Duration,
) ([]*models.CheckJob, time.Duration, error) {
	return b.claim(ctx, fastLimit, "")
}

// ClaimJobsForCheck is the agent express path: a claim pinned to one check.
// The next-eligible hint is dropped — the express goroutine doesn't poll.
func (b *WSBackend) ClaimJobsForCheck(
	ctx context.Context,
	_ string,
	_ *string,
	checkUID string,
) ([]*models.CheckJob, error) {
	jobs, _, err := b.claim(ctx, 4, checkUID)

	return jobs, err
}

// claim sends one claim frame and converts/unseals the response. The second
// return is the server's next-eligible hint (0 when absent — e.g. an older
// server that predates retryInMs).
func (b *WSBackend) claim(
	ctx context.Context, maxJobs int, checkUID string,
) ([]*models.CheckJob, time.Duration, error) {
	// The claim frame doubles as this agent's capability report: it is the only
	// frame an idle-but-connected agent sends regularly, and Heartbeat below is
	// a no-op. The probe behind Current() is cached for well under a heartbeat,
	// so a claim-per-second costs nothing while a host that gains or loses IPv6
	// still converges without a restart.
	egress := egressreport.Current()

	resp, err := b.request(ctx, &agents.ClientFrame{
		Type:       agents.MsgTypeClaim,
		MaxJobs:    maxJobs,
		CheckUID:   checkUID,
		EgressIPv4: egress.IPv4,
		EgressIPv6: egress.IPv6,
	})
	if err != nil {
		return nil, 0, err
	}

	jobs := make([]*models.CheckJob, 0, len(resp.Jobs))

	for i := range resp.Jobs {
		wireJob := resp.Jobs[i]
		job := wireJob.ToCheckJob()

		if job.ConfigSealed != nil && *job.ConfigSealed != "" {
			secrets, unsealErr := credentials.UnsealWithIdentity(b.identityX25519(), *job.ConfigSealed)
			if unsealErr != nil {
				// The spec's decrypt-failure contract: a clear job error, not a
				// silent skip — the operator sees WHY the check failed and the
				// fix (re-save the credentials).
				b.logger.WarnContext(ctx, "sealed credentials not addressed to this agent",
					"check_uid", job.CheckUID, "error", unsealErr)
				b.submitSealError(ctx, job)

				continue
			}

			job.Config = models.JSONMap(credentials.MergeConfig(job.Config, secrets))
			job.ConfigSealed = nil // plaintext stays in memory only
		}

		// A tunneled job carries its SSH check's sealed endpoint: unseal it with
		// the agent identity next to the job's own envelope, keep the plaintext
		// snapshot in memory only, and register it so the worker loop's resolver
		// can find it. Unseal failure is reported like a job-envelope failure —
		// a clear error result, dropped from the batch, never run half-armed.
		if wireJob.Tunnel != nil {
			cfg, tunErr := buildTunnelConfig(b.identityX25519(), wireJob.Tunnel)
			if tunErr != nil {
				b.logger.WarnContext(ctx, "ssh tunnel not available on this agent",
					"check_uid", job.CheckUID, "tunnel_check_uid", wireJob.Tunnel.CheckUID, "error", tunErr)
				b.submitTunnelError(ctx, job, tunErr)

				continue
			}

			b.registerTunnel(wireJob.Tunnel.CheckUID, cfg)
		}

		jobs = append(jobs, job)
	}

	return jobs, time.Duration(resp.RetryInMs) * time.Millisecond, nil
}

// submitSealError reports a decrypt failure as an error result so the check's
// history shows the actionable message instead of silently never running.
func (b *WSBackend) submitSealError(ctx context.Context, job *models.CheckJob) {
	_, _ = b.request(ctx, &agents.ClientFrame{
		Type:     agents.MsgTypeResult,
		JobUID:   job.UID,
		Status:   int(models.ResultStatusError),
		Duration: 0,
		Output:   map[string]any{"error": ErrSealedForOthers.Error()},
	})
}

// SubmitResult submits one result frame and waits for the ack. The server
// performs the save/incident/release sequence; the agent-side scheduling
// state is advisory only (the server recomputes the next tick itself).
func (b *WSBackend) SubmitResult(
	ctx context.Context,
	job *models.CheckJob,
	_ string,
	req *SubmitResultRequest,
) error {
	// Release the in-memory tunnel snapshot when a tunneled job finishes — its
	// lifecycle is bound to the job. Safe on non-tunneled jobs (no-op) and on a
	// tunnel that was never registered (a missing-block drop unregisters nothing).
	if tunnelUID, ok := checkerdef.TunnelCheckUIDFrom(job.Config); ok {
		defer b.unregisterTunnel(tunnelUID)
	}

	// execStart travels with the result so the SERVER can compute the delay
	// sample for its hoisted accounting (spec 2026-07-27-01): the agent-side
	// Sched block is deliberately not sent — the server recomputes the whole
	// scheduling state from the result it just persisted.
	frame := &agents.ClientFrame{
		Type:     agents.MsgTypeResult,
		JobUID:   job.UID,
		Status:   req.Status,
		Duration: req.Duration,
		Metrics:  req.Metrics,
		Output:   req.Output,
	}

	if !req.ExecStart.IsZero() {
		execStart := req.ExecStart
		frame.ExecStart = &execStart
	}

	_, err := b.request(ctx, frame)

	return err
}

// ReleaseLease is unused agent-side (the entitlements deferral is enforced at
// the server's dispatch); a lease the agent abandons simply expires.
func (b *WSBackend) ReleaseLease(_ context.Context, _ *models.CheckJob, _ string, _ time.Time) error {
	return nil
}

// LastResults is unsupported: passive checks (heartbeat/email) are driven by
// inbound signals to the server and never dispatch to private regions' agents.
func (b *WSBackend) LastResults(_ context.Context, _ string, _ []string) (map[string]*models.Result, error) {
	return nil, ErrPassiveUnsupported
}

// Hints returns a fresh subscription to jobs-available frames.
func (b *WSBackend) Hints() <-chan string {
	hintCh := make(chan string, 4)

	b.mu.Lock()
	b.hints = append(b.hints, hintCh)
	b.mu.Unlock()

	return hintCh
}

// identityX25519 returns the agent's age identity string.
func (b *WSBackend) identityX25519() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.identity.X25519Identity
}

// request sends one frame and waits for its correlated response.
func (b *WSBackend) request(ctx context.Context, frame *agents.ClientFrame) (*agents.ServerFrame, error) {
	if err := b.ensureConn(ctx); err != nil {
		return nil, err
	}

	id, err := randomID()
	if err != nil {
		return nil, err
	}

	frame.ID = id
	respCh := make(chan agents.ServerFrame, 1)

	b.mu.Lock()
	conn := b.conn
	if conn == nil {
		b.mu.Unlock()

		return nil, ErrConnLost
	}
	b.pending[id] = respCh
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}()

	writeCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	if err := wsjson.Write(writeCtx, conn, frame); err != nil {
		b.retire(ctx, conn, "write error", err)

		return nil, fmt.Errorf("agent ws write: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(requestTimeout):
		return nil, ErrRequestTimeout
	case resp := <-respCh:
		if resp.Type == agents.MsgTypeError {
			if resp.Code == codeConnLost {
				return nil, ErrConnLost
			}

			return nil, fmt.Errorf("%w: %s (%s)", ErrServerError, resp.Title, resp.Code)
		}

		return &resp, nil
	}
}

// ensureConn dials (enrolling if needed) when no live connection exists. The
// first call also starts the reconnect supervisor, capturing the long-lived
// worker ctx (Register is always the first caller) as a goroutine parameter so
// nothing needs to store a context on the struct.
func (b *WSBackend) ensureConn(ctx context.Context) error {
	b.mu.Lock()
	if !b.supervisorStarted {
		b.supervisorStarted = true

		go b.reconnectSupervisor(ctx)
	}

	conn := b.conn
	b.mu.Unlock()

	if conn != nil {
		return nil
	}

	return b.dialOnce(ctx, true)
}

// dialOnce establishes one connection under dialMu so only a single dial ever
// runs at a time, whichever path triggered it. When paced, it enforces the
// lastDial min-spacing (the lazy request() path); the reconnect supervisor
// passes paced=false because it owns its own backoff schedule.
func (b *WSBackend) dialOnce(ctx context.Context, paced bool) error {
	b.dialMu.Lock()
	defer b.dialMu.Unlock()

	b.mu.Lock()
	if b.conn != nil {
		b.mu.Unlock()

		return nil
	}

	lastDial := b.lastDial
	enrolled := b.identity.AgentUID != ""
	b.mu.Unlock()

	if paced {
		if since := time.Since(lastDial); since < reconnectBackoff {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(reconnectBackoff - since):
			}

			b.mu.Lock()
			connected := b.conn != nil
			b.mu.Unlock()

			if connected {
				return nil
			}
		}
	}

	b.mu.Lock()
	b.lastDial = time.Now()
	b.mu.Unlock()

	if enrolled {
		return b.dialReconnect(ctx)
	}

	return b.dialEnroll(ctx)
}

// reconnectSupervisor is the single owner of proactive reconnection. It idles
// until a drop wakes it, then reconnects with backoff, and stops on ctx
// cancellation. A buffered reconnectSignal coalesces drops so one that races an
// in-progress reconnect is re-checked rather than lost.
func (b *WSBackend) reconnectSupervisor(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-b.reconnectSignal:
		}

		b.reconnectUntilConnected(ctx)
	}
}

// reconnectUntilConnected dials with exponential backoff (first attempt
// immediate) until the link is restored or ctx is canceled.
func (b *WSBackend) reconnectUntilConnected(ctx context.Context) {
	var delay time.Duration

	for {
		b.mu.Lock()
		connected := b.conn != nil
		b.mu.Unlock()

		if connected || ctx.Err() != nil {
			return
		}

		if delay > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}

		err := b.dialOnce(ctx, false)
		if err == nil {
			return
		}

		delay = nextReconnectDelay(delay)
		b.logger.WarnContext(ctx, "agent reconnect attempt failed", "error", err, "retry_in", delay)
	}
}

// nextReconnectDelay doubles the previous delay with a reconnectBackoff floor, a
// maxReconnectBackoff cap, and ±20% jitter.
func nextReconnectDelay(prev time.Duration) time.Duration {
	next := prev * 2
	if next < reconnectBackoff {
		next = reconnectBackoff
	}

	if next > maxReconnectBackoff {
		next = maxReconnectBackoff
	}

	return jitterDuration(next)
}

// jitterDuration applies ±20% jitter using the crypto/rand source already
// imported for correlation ids.
func jitterDuration(base time.Duration) time.Duration {
	span := int64(base) / 5
	if span <= 0 {
		return base
	}

	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return base
	}

	delta := int64(binary.BigEndian.Uint64(buf[:])%uint64(2*span)) - span

	return base + time.Duration(delta)
}

// dialEnroll performs the first connection: enrollment bearer, keypair public
// halves in the enroll frame, identity persisted on success.
func (b *WSBackend) dialEnroll(ctx context.Context) error {
	if b.enrollmentToken == "" {
		return ErrEnrollNoToken
	}

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+b.enrollmentToken)

	conn, err := b.dial(ctx, headers)
	if err != nil {
		return err
	}

	// hello (no identity yet)
	var hello agents.ServerFrame
	if err := wsjson.Read(ctx, conn, &hello); err != nil {
		_ = conn.Close(websocket.StatusProtocolError, "no hello")

		return fmt.Errorf("read hello: %w", err)
	}

	if err := wsjson.Write(ctx, conn, agents.ClientFrame{
		Type:             agents.MsgTypeEnroll,
		Name:             b.name,
		Ed25519PublicKey: b.identity.Ed25519PublicKey,
		X25519PublicKey:  b.identity.X25519Recipient,
	}); err != nil {
		_ = conn.Close(websocket.StatusProtocolError, "enroll write failed")

		return fmt.Errorf("write enroll: %w", err)
	}

	var enrolled agents.ServerFrame
	if err := wsjson.Read(ctx, conn, &enrolled); err != nil || enrolled.Type != agents.MsgTypeEnrolled {
		_ = conn.Close(CloseProtocolError, "no enrolled ack")

		return fmt.Errorf("enrollment rejected: %w", err)
	}

	// Second hello carries the identity echo; consume it before the pump owns
	// the read side.
	var identityHello agents.ServerFrame
	if err := wsjson.Read(ctx, conn, &identityHello); err != nil {
		_ = conn.Close(websocket.StatusProtocolError, "no identity hello")

		return fmt.Errorf("read identity hello: %w", err)
	}

	b.mu.Lock()
	b.identity.AgentUID = enrolled.AgentUID
	b.identity.Region = enrolled.Region
	identityCopy := *b.identity
	b.mu.Unlock()

	b.activateConn(ctx, conn, enrolled.WorkerUID)

	if b.onIdentityChange != nil {
		b.onIdentityChange(&identityCopy)
	}

	b.logger.InfoContext(ctx, "agent enrolled",
		"agent_uid", enrolled.AgentUID,
		"region", enrolled.Region,
		"fingerprint", enrolled.Fingerprint)

	return nil
}

// activateConn publishes a freshly dialed connection and starts its read pump
// and keepalive ping loop. Both loops run on WithoutCancel so a per-request ctx
// cancellation cannot tear down the shared connection; they end when the conn
// is retired (read error, ping failure, or an explicit drop).
func (b *WSBackend) activateConn(ctx context.Context, conn *websocket.Conn, workerUID string) {
	pumpCtx := context.WithoutCancel(ctx)

	b.mu.Lock()
	b.workerUID = workerUID
	b.conn = conn
	b.connectedAt = time.Now()
	b.mu.Unlock()

	go b.readPump(pumpCtx, conn)
	go b.pingLoop(pumpCtx, conn)
}

// dialReconnect performs a signed-header reconnect.
func (b *WSBackend) dialReconnect(ctx context.Context) error {
	b.mu.Lock()
	identity := *b.identity
	b.mu.Unlock()

	timestamp := time.Now().UTC().Format(time.RFC3339)

	nonce, err := randomID()
	if err != nil {
		return err
	}

	signature, err := identity.Sign(http.MethodGet, wsPath, timestamp, nonce)
	if err != nil {
		return fmt.Errorf("sign reconnect: %w", err)
	}

	headers := http.Header{}
	headers.Set("X-Sp-Agent-Uid", identity.AgentUID)
	headers.Set("X-Sp-Timestamp", timestamp)
	headers.Set("X-Sp-Nonce", nonce)
	headers.Set("X-Sp-Signature", signature)

	conn, err := b.dial(ctx, headers)
	if err != nil {
		return err
	}

	var hello agents.ServerFrame
	if err := wsjson.Read(ctx, conn, &hello); err != nil || hello.Type != agents.MsgTypeHello {
		_ = conn.Close(websocket.StatusProtocolError, "no hello")

		return fmt.Errorf("read hello: %w", err)
	}

	b.activateConn(ctx, conn, hello.WorkerUID)

	b.logger.InfoContext(ctx, "agent reconnected", "agent_uid", identity.AgentUID, "region", hello.Region)

	return nil
}

// dial opens the raw WebSocket.
func (b *WSBackend) dial(ctx context.Context, headers http.Header) (*websocket.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	conn, resp, err := websocket.Dial(dialCtx, b.serverURL+wsPath, &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}

		return nil, fmt.Errorf("dial %s (status %d): %w", wsPath, status, err)
	}

	conn.SetReadLimit(maxFrameBytes)

	return conn, nil
}

// maxFrameBytes mirrors the server-side inbound cap for symmetric limits.
const maxFrameBytes = 256 * 1024

// CloseProtocolError mirrors the server's protocol-error close code.
const CloseProtocolError websocket.StatusCode = 4400

// readPump dispatches inbound frames: correlated responses to their waiters,
// jobs-available to every hint subscriber. Ends (and retires the connection) on
// the first read error.
func (b *WSBackend) readPump(ctx context.Context, conn *websocket.Conn) {
	for {
		var frame agents.ServerFrame
		if err := wsjson.Read(ctx, conn, &frame); err != nil {
			b.retire(ctx, conn, "read error", err)

			return
		}

		switch {
		case frame.Type == agents.MsgTypeJobsAvailable:
			b.mu.Lock()
			for _, hintCh := range b.hints {
				select {
				case hintCh <- "{}":
				default: // slow consumer: drop, the periodic poll catches up
				}
			}
			b.mu.Unlock()
		case frame.ID != "":
			b.mu.Lock()
			waiter, ok := b.pending[frame.ID]
			b.mu.Unlock()

			if ok {
				select {
				case waiter <- frame:
				default:
				}
			}
		default:
			// Unsolicited frame without correlation (e.g. hello echo) — ignore.
		}
	}
}

// pingLoop keepalives one connection: a ping every pingInterval with a short
// timeout. A failed ping means a half-open link — retire the conn and let the
// supervisor reconnect. Bounds detection latency to seconds instead of the OS
// TCP timeout. Ends when the conn is no longer the active one.
func (b *WSBackend) pingLoop(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(b.pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		pingCtx, cancel := context.WithTimeout(ctx, b.pingInterval/2)
		err := conn.Ping(pingCtx)

		cancel()

		if err != nil {
			b.retire(ctx, conn, "ping failed", err)

			return
		}

		b.mu.Lock()
		current := b.conn == conn
		b.mu.Unlock()

		if !current {
			return
		}
	}
}

// retire tears down conn: it is idempotent per connection (it acts only while
// conn is still the active one). It logs the loss at Warn with the uptime, wakes
// every in-flight waiter with a CONN_LOST error so requests fail fast instead of
// waiting out requestTimeout, and signals the reconnect supervisor.
func (b *WSBackend) retire(ctx context.Context, conn *websocket.Conn, reason string, cause error) {
	// Close asynchronously: on a half-open link (exactly the case the liveness
	// ping exists to catch) the peer never answers the close handshake, and the
	// library blocks for several seconds waiting for it. Backgrounding the
	// close keeps marking the conn dead, waking waiters, and kicking the
	// reconnect supervisor immediate regardless of whether the peer replies.
	go func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	b.mu.Lock()
	if b.conn != conn {
		b.mu.Unlock()

		return
	}

	b.conn = nil
	uptime := time.Since(b.connectedAt)

	waiters := make([]chan agents.ServerFrame, 0, len(b.pending))
	for _, waiter := range b.pending {
		waiters = append(waiters, waiter)
	}

	b.pending = map[string]chan agents.ServerFrame{}
	signal := b.reconnectSignal
	b.mu.Unlock()

	b.logger.WarnContext(ctx, "agent connection lost",
		"reason", reason,
		"error", cause,
		"uptime", uptime.Round(time.Millisecond).String())

	for _, waiter := range waiters {
		select {
		case waiter <- agents.ServerFrame{Type: agents.MsgTypeError, Code: codeConnLost}:
		default:
		}
	}

	if signal != nil {
		select {
		case signal <- struct{}{}:
		default:
		}
	}
}

// randomID returns a 16-hex-char correlation id / nonce.
func randomID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("random id: %w", err)
	}

	return hex.EncodeToString(buf), nil
}
