package agentattach

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Signed request headers. Identical to the WS upgrade's — an agent signs one
// challenge shape, whichever route it is talking to.
const (
	headerAgentUID  = "X-Sp-Agent-Uid"
	headerTimestamp = "X-Sp-Timestamp"
	headerNonce     = "X-Sp-Nonce"
	headerSignature = "X-Sp-Signature"
	// headerTopic carries the attachment topic. A header rather than a query
	// parameter so it never lands in an access log alongside the uid it names.
	headerTopic = "X-Sp-Attachment-Topic"
)

// mimeTypePNG is the only attachment format on the allowlist today. Declared
// once because the sniffer, the stored row and the tests must not be able to
// disagree about what it is.
const mimeTypePNG = "image/png"

// MaxUploadBytes caps one upload. It mirrors checkerdef.MaxScreenshotBytes —
// the capturing side already refuses to hold more than this in memory, so an
// agent that sends more is either broken or hostile, and either way the body
// is refused rather than trusted.
const MaxUploadBytes int64 = 4 * 1024 * 1024

// nonceRetention keeps a consumed nonce for twice the accepted clock skew, so
// a captured signature cannot be replayed anywhere in the window it is valid.
// Same value the WS upgrade uses, for the same reason.
const nonceRetention = 2 * agentcrypto.DefaultClockSkew

// Error codes this endpoint returns.
const (
	errCodeBadTopic   base.ErrorCode = "BAD_ATTACHMENT_TOPIC"
	errCodeBadContent base.ErrorCode = "BAD_ATTACHMENT_CONTENT"
	errCodeTooLarge   base.ErrorCode = "ATTACHMENT_TOO_LARGE"
	errCodeRateLimit  base.ErrorCode = "RATE_LIMITED"
)

// AttachmentWriter is the storage side: writes bytes under a topic and returns
// the new file's UID. Satisfied by files.AttachmentStore.
type AttachmentWriter interface {
	CreateAttachment(
		ctx context.Context, orgUID uuid.UUID, name, mimeType, topic string,
		details models.JSONMap, body []byte,
	) (string, error)
}

// Handler serves POST /api/v1/agent/attachments.
type Handler struct {
	base.HandlerBase
	dbService db.Service
	writer    AttachmentWriter
	registry  *Registry
	nonces    agentcrypto.NonceGuard
	limiter   *rateLimiter
	logger    *slog.Logger
}

// NewHandler builds the agent attachment upload handler.
func NewHandler(
	cfg *config.Config,
	dbService db.Service,
	writer AttachmentWriter,
	registry *Registry,
	nonces agentcrypto.NonceGuard,
	logger *slog.Logger,
) *Handler {
	if logger == nil {
		logger = slog.Default()
	}

	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		dbService:   dbService,
		writer:      writer,
		registry:    registry,
		nonces:      nonces,
		limiter:     newRateLimiter(),
		logger:      logger,
	}
}

// Upload handles POST /api/v1/agent/attachments.
//
// The order of the checks below is deliberate and is the security story of
// this endpoint:
//
//  1. authenticate the agent (signature, skew, replay) — nothing else runs
//     for an unauthenticated caller;
//  2. rate-limit per AGENT, not per IP, so one compromised agent cannot use
//     a NAT pool to buy more quota;
//  3. authorize the TOPIC against the agent's own binding — never against
//     anything in the request;
//  4. only then read the body, capped, and sniff its real type.
//
// Reading the body last matters: a rejected request never costs more than its
// headers, so this route cannot be used to push megabytes at the server on an
// unauthenticated or unauthorized path.
func (h *Handler) Upload(writer http.ResponseWriter, req *http.Request) error {
	ctx := req.Context()

	agent, authErr := h.authenticate(ctx, req)
	if authErr != nil {
		return h.WriteError(writer, authErr.status, authErr.code, authErr.message)
	}

	if !h.limiter.allow(agent.UID, time.Now()) {
		return h.WriteError(writer, http.StatusTooManyRequests, errCodeRateLimit,
			"Too many attachment uploads from this agent")
	}

	topic := req.Header.Get(headerTopic)

	decision, err := h.registry.Authorize(ctx, scopeFor(agent), topic)
	if err != nil {
		// One answer for every topic failure. Distinguishing "malformed" from
		// "not yours" would tell a probing agent which incident uids exist in
		// other tenants; the server logs the real reason instead.
		h.logger.WarnContext(ctx, "agent attachment topic rejected",
			"agent", agent.UID, "topic", topic, "error", err)

		return h.WriteError(writer, http.StatusForbidden, errCodeBadTopic,
			"Attachment topic is not valid for this agent")
	}

	body, readErr := readCappedBody(req)
	if readErr != nil {
		if errors.Is(readErr, errBodyTooLarge) {
			return h.WriteError(writer, http.StatusRequestEntityTooLarge, errCodeTooLarge,
				"Attachment exceeds the maximum upload size")
		}

		return h.WriteError(writer, http.StatusBadRequest, errCodeBadContent,
			"Attachment body could not be read")
	}

	mimeType, ok := sniffMIME(req.Header.Get("Content-Type"), body)
	if !ok {
		return h.WriteError(writer, http.StatusUnsupportedMediaType, errCodeBadContent,
			"Attachment content type is not allowed, or does not match the bytes sent")
	}

	orgUID, parseErr := uuid.Parse(decision.OrgUID)
	if parseErr != nil {
		return h.WriteInternalError(writer, req, parseErr)
	}

	// Already validated by the registry above; the parse cannot fail here.
	parts, _ := ParseTopic(topic)

	details := models.JSONMap{
		models.AttachmentDetailCapturedAt: time.Now(),
		models.AttachmentDetailRegion:     agent.Region,
		models.AttachmentDetailTrigger:    models.AttachmentTriggerAgentUpload,
	}

	fileUID, createErr := h.writer.CreateAttachment(
		ctx, orgUID, parts.Kind+".png", mimeType, topic, details, body,
	)
	if createErr != nil {
		return h.WriteInternalError(writer, req, createErr)
	}

	h.logger.InfoContext(ctx, "agent attachment stored",
		"agent", agent.UID, "topic", topic, "fileUid", fileUID, "bytes", len(body))

	return h.WriteJSON(writer, http.StatusCreated, UploadResponse{FileUID: fileUID})
}

// UploadResponse is what the agent gets back: the file's UID, which it
// references from its result instead of carrying the bytes.
type UploadResponse struct {
	FileUID string `json:"fileUid"`
}

// scopeFor projects an authenticated agent row onto the authorizer's view of
// it. Every field comes from the row; none from the request.
func scopeFor(agent *models.Agent) UploaderScope {
	return UploaderScope{
		OrgUID: agent.OrgUID(),
		Region: agent.Region,
		System: agent.IsSystem(),
	}
}

// authError carries a rejection with the status and code to answer it with.
type authError struct {
	status  int
	code    base.ErrorCode
	message string
}

func (e *authError) Error() string { return e.message }

// authenticate verifies the signed agent headers. Identical in substance to
// the WS upgrade's serveReconnect: same challenge, same ±5 min skew, same
// cluster-wide replay guard, same fail-closed behavior on a store error.
func (h *Handler) authenticate(ctx context.Context, req *http.Request) (*models.Agent, *authError) {
	agentUID := req.Header.Get(headerAgentUID)
	timestamp := req.Header.Get(headerTimestamp)
	nonce := req.Header.Get(headerNonce)
	signature := req.Header.Get(headerSignature)

	if agentUID == "" || timestamp == "" || nonce == "" || signature == "" {
		return nil, &authError{http.StatusUnauthorized, base.ErrorCodeNoToken, "Missing signed agent headers"}
	}

	agent, err := h.dbService.GetAgent(ctx, agentUID)
	if err != nil || agent == nil {
		return nil, &authError{http.StatusUnauthorized, base.ErrorCodeInvalidToken, "Unknown agent"}
	}

	if agent.Status != models.AgentStatusActive {
		return nil, &authError{http.StatusForbidden, base.ErrorCodeForbidden, "Agent has been revoked"}
	}

	if tsErr := agentcrypto.ValidateTimestamp(timestamp, time.Now(), agentcrypto.DefaultClockSkew); tsErr != nil {
		return nil, &authError{
			http.StatusUnauthorized, base.ErrorCodeInvalidToken,
			"Request timestamp outside the accepted clock skew",
		}
	}

	// Fails CLOSED: a nonce store that cannot answer rejects the upload, which
	// the agent retries. A replay-guard outage must never become a replay
	// window.
	if nonceErr := h.nonces.CheckAndStore(
		ctx, agent.UID, nonce, time.Now(), nonceRetention,
	); nonceErr != nil {
		if !errors.Is(nonceErr, agentcrypto.ErrReplayedNonce) {
			h.logger.ErrorContext(ctx, "agent nonce store failed", "error", nonceErr, "agent", agent.UID)
		}

		return nil, &authError{http.StatusUnauthorized, base.ErrorCodeInvalidToken, "Nonce already used"}
	}

	if sigErr := agentcrypto.VerifySignature(
		agent.Ed25519PublicKey, req.Method, req.URL.Path, timestamp, nonce, signature,
	); sigErr != nil {
		return nil, &authError{http.StatusUnauthorized, base.ErrorCodeInvalidToken, "Invalid signature"}
	}

	return agent, nil
}

// Body-read failures, distinguished so the caller can answer 413 for an
// over-cap upload rather than lumping it in with an unreadable one.
var (
	errBodyTooLarge = errors.New("attachment body exceeds the cap")
	errBodyEmpty    = errors.New("attachment body is empty")
)

// readCappedBody reads at most MaxUploadBytes+1 bytes and reports an
// over-cap body rather than silently truncating it — a truncated PNG is a
// corrupt file, and storing one would be worse than storing nothing.
func readCappedBody(req *http.Request) ([]byte, error) {
	limited := io.LimitReader(req.Body, MaxUploadBytes+1)

	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}

	if int64(len(body)) > MaxUploadBytes {
		return nil, errBodyTooLarge
	}

	if len(body) == 0 {
		return nil, errBodyEmpty
	}

	return body, nil
}

// pngMagic is the 8-byte PNG signature.
//
//nolint:gochecknoglobals // an immutable byte constant Go cannot express as const
var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// sniffMIME enforces the format allowlist against the BYTES, not the header.
//
// The declared Content-Type is a claim by an agent, and an agent is a machine
// somebody else runs; believing it would let a caller store an HTML document
// as `image/png` and have it served back from our own origin — stored XSS, the
// exact hazard files.safeInlineMIME already documents. So the header must be
// on the allowlist AND the magic bytes must agree with it.
//
// image/png only, today. Widening the list means adding a sniffer for the new
// format, never relaxing this rule.
func sniffMIME(declared string, body []byte) (string, bool) {
	if idx := strings.IndexByte(declared, ';'); idx >= 0 {
		declared = declared[:idx]
	}

	declared = strings.TrimSpace(declared)

	if declared != "" && declared != mimeTypePNG {
		return "", false
	}

	if !bytes.HasPrefix(body, pngMagic) {
		return "", false
	}

	return mimeTypePNG, true
}

// rateLimiter is a per-agent token bucket.
//
// Per AGENT rather than per IP on purpose: the identity this endpoint trusts
// is the signature, and a fleet behind one NAT must not share — or be able to
// exhaust — each other's quota.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

// uploadBurst and uploadRefill size the bucket. An agent uploads once per
// incident transition, so a burst of ten with one token refilled per minute is
// generous for real traffic and still bounds a runaway agent to ~60 blobs an
// hour.
const (
	uploadBurst  = 10
	uploadRefill = time.Minute
)

type bucket struct {
	tokens     int
	lastRefill time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{buckets: map[string]*bucket{}}
}

// allow consumes one token for agentUID, refilling first. Reports whether the
// request may proceed.
func (l *rateLimiter) allow(agentUID string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	agentBucket, ok := l.buckets[agentUID]
	if !ok {
		l.buckets[agentUID] = &bucket{tokens: uploadBurst - 1, lastRefill: now}

		return true
	}

	elapsed := now.Sub(agentBucket.lastRefill)
	if refill := int(elapsed / uploadRefill); refill > 0 {
		agentBucket.tokens += refill
		if agentBucket.tokens > uploadBurst {
			agentBucket.tokens = uploadBurst
		}

		agentBucket.lastRefill = now
	}

	if agentBucket.tokens <= 0 {
		return false
	}

	agentBucket.tokens--

	return true
}
