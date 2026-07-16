package backend

import (
	"context"
	"crypto/rand"
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
	// ErrSealedForOthers is surfaced as the job error when a sealed blob cannot
	// be decrypted by this agent — the fix is a credentials re-save.
	ErrSealedForOthers = errors.New(
		"credentials not sealed for this agent — re-save the check's credentials")
)

const (
	// wsPath is the agent transport endpoint.
	wsPath = "/api/v1/agent/ws"
	// requestTimeout bounds one claim/result round-trip.
	requestTimeout = 30 * time.Second
	// reconnectBackoff paces reconnection attempts.
	reconnectBackoff = 5 * time.Second
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

	mu        sync.Mutex
	identity  *Identity
	conn      *websocket.Conn
	workerUID string
	pending   map[string]chan agents.ServerFrame
	hints     []chan string
	lastDial  time.Time

	// onIdentityChange persists the identity after enrollment.
	onIdentityChange func(*Identity)

	logger *slog.Logger
}

// NewWSBackend creates a WSBackend. identity must carry the agent's keypairs;
// AgentUID/Region may be empty (enrollment fills them, then onIdentityChange
// fires so the caller can persist).
func NewWSBackend(
	serverURL, enrollmentToken, name string,
	identity *Identity,
	onIdentityChange func(*Identity),
) *WSBackend {
	return &WSBackend{
		serverURL:        serverURL,
		enrollmentToken:  enrollmentToken,
		name:             name,
		identity:         identity,
		pending:          map[string]chan agents.ServerFrame{},
		onIdentityChange: onIdentityChange,
		logger:           slog.Default().With("component", "agent_ws_backend"),
	}
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

// Heartbeat is a no-op: the server refreshes last_seen_at on pings and frames.
func (b *WSBackend) Heartbeat(_ context.Context, _ string) error {
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
) ([]*models.CheckJob, error) {
	return b.claim(ctx, fastLimit, "")
}

// ClaimJobsForCheck is the agent express path: a claim pinned to one check.
func (b *WSBackend) ClaimJobsForCheck(
	ctx context.Context,
	_ string,
	_ *string,
	checkUID string,
) ([]*models.CheckJob, error) {
	return b.claim(ctx, 4, checkUID)
}

// claim sends one claim frame and converts/unseals the response.
func (b *WSBackend) claim(ctx context.Context, maxJobs int, checkUID string) ([]*models.CheckJob, error) {
	resp, err := b.request(ctx, agents.ClientFrame{
		Type:     agents.MsgTypeClaim,
		MaxJobs:  maxJobs,
		CheckUID: checkUID,
	})
	if err != nil {
		return nil, err
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
				b.logger.Warn("sealed credentials not addressed to this agent",
					"check_uid", job.CheckUID, "error", unsealErr)
				b.submitSealError(ctx, job)

				continue
			}

			job.Config = models.JSONMap(credentials.MergeConfig(job.Config, secrets))
			job.ConfigSealed = nil // plaintext stays in memory only
		}

		jobs = append(jobs, job)
	}

	return jobs, nil
}

// submitSealError reports a decrypt failure as an error result so the check's
// history shows the actionable message instead of silently never running.
func (b *WSBackend) submitSealError(ctx context.Context, job *models.CheckJob) {
	_, _ = b.request(ctx, agents.ClientFrame{
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
	_, err := b.request(ctx, agents.ClientFrame{
		Type:     agents.MsgTypeResult,
		JobUID:   job.UID,
		Status:   req.Status,
		Duration: req.Duration,
		Metrics:  req.Metrics,
		Output:   req.Output,
	})

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
	ch := make(chan string, 4)

	b.mu.Lock()
	b.hints = append(b.hints, ch)
	b.mu.Unlock()

	return ch
}

// identityX25519 returns the agent's age identity string.
func (b *WSBackend) identityX25519() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.identity.X25519Identity
}

// request sends one frame and waits for its correlated response.
func (b *WSBackend) request(ctx context.Context, frame agents.ClientFrame) (*agents.ServerFrame, error) {
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
		b.dropConn(conn)

		return nil, fmt.Errorf("agent ws write: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(requestTimeout):
		return nil, ErrRequestTimeout
	case resp := <-respCh:
		if resp.Type == agents.MsgTypeError {
			return nil, fmt.Errorf("%w: %s (%s)", ErrServerError, resp.Title, resp.Code)
		}

		return &resp, nil
	}
}

// ensureConn dials (enrolling if needed) when no live connection exists.
// Reconnection attempts are paced by reconnectBackoff.
func (b *WSBackend) ensureConn(ctx context.Context) error {
	b.mu.Lock()
	if b.conn != nil {
		b.mu.Unlock()

		return nil
	}

	if since := time.Since(b.lastDial); since < reconnectBackoff {
		b.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(reconnectBackoff - since):
		}

		b.mu.Lock()
		if b.conn != nil {
			b.mu.Unlock()

			return nil
		}
	}

	b.lastDial = time.Now()
	enrolled := b.identity.AgentUID != ""
	b.mu.Unlock()

	if enrolled {
		return b.dialReconnect(ctx)
	}

	return b.dialEnroll(ctx)
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
	b.workerUID = enrolled.WorkerUID
	b.conn = conn
	identityCopy := *b.identity
	b.mu.Unlock()

	if b.onIdentityChange != nil {
		b.onIdentityChange(&identityCopy)
	}

	b.logger.Info("agent enrolled",
		"agent_uid", enrolled.AgentUID,
		"region", enrolled.Region,
		"fingerprint", enrolled.Fingerprint)

	go b.readPump(conn)

	return nil
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
	headers.Set("X-SP-Agent-Uid", identity.AgentUID)
	headers.Set("X-SP-Timestamp", timestamp)
	headers.Set("X-SP-Nonce", nonce)
	headers.Set("X-SP-Signature", signature)

	conn, err := b.dial(ctx, headers)
	if err != nil {
		return err
	}

	var hello agents.ServerFrame
	if err := wsjson.Read(ctx, conn, &hello); err != nil || hello.Type != agents.MsgTypeHello {
		_ = conn.Close(websocket.StatusProtocolError, "no hello")

		return fmt.Errorf("read hello: %w", err)
	}

	b.mu.Lock()
	b.workerUID = hello.WorkerUID
	b.conn = conn
	b.mu.Unlock()

	b.logger.Info("agent reconnected", "agent_uid", identity.AgentUID, "region", hello.Region)

	go b.readPump(conn)

	return nil
}

// dial opens the raw WebSocket.
func (b *WSBackend) dial(ctx context.Context, headers http.Header) (*websocket.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	conn, resp, err := websocket.Dial(dialCtx, b.serverURL+wsPath, &websocket.DialOptions{
		HTTPHeader: headers,
	})
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
// jobs-available to every hint subscriber. Ends (and drops the connection) on
// the first read error.
func (b *WSBackend) readPump(conn *websocket.Conn) {
	ctx := context.Background()

	for {
		var frame agents.ServerFrame
		if err := wsjson.Read(ctx, conn, &frame); err != nil {
			b.dropConn(conn)

			return
		}

		switch {
		case frame.Type == agents.MsgTypeJobsAvailable:
			b.mu.Lock()
			for _, ch := range b.hints {
				select {
				case ch <- "{}":
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

// dropConn clears the active connection if it is still the given one.
func (b *WSBackend) dropConn(conn *websocket.Conn) {
	_ = conn.Close(websocket.StatusNormalClosure, "")

	b.mu.Lock()
	if b.conn == conn {
		b.conn = nil
	}
	b.mu.Unlock()
}

// randomID returns a 16-hex-char correlation id / nonce.
func randomID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("random id: %w", err)
	}

	return hex.EncodeToString(buf), nil
}
