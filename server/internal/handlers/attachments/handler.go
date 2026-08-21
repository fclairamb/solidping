package attachments

import (
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Signed-request headers. Identical to the agent WebSocket reconnect headers —
// same scheme, same skew, same replay guard — because an agent that can prove
// it is itself for a socket upgrade can prove it the same way for an upload.
// Reusing the scheme is the point: no second credential, no bearer token, and
// no S3 credentials on an agent, ever.
const (
	headerAgentUID  = "X-Sp-Agent-Uid"
	headerTimestamp = "X-Sp-Timestamp"
	headerNonce     = "X-Sp-Nonce"
	headerSignature = "X-Sp-Signature"
)

// nonceRetention mirrors agentws: twice the accepted clock skew, so a captured
// signature cannot be replayed anywhere inside the window it is valid in.
const nonceRetention = 2 * agents.DefaultClockSkew

// uploadRateWindow / uploadRateBurst bound how often one agent may upload.
//
// Generous relative to the real traffic (attachments are minted on incident
// transitions, not per probe) and small in absolute terms, because the cost
// being bounded is storage, not CPU: an agent that has been compromised should
// not be able to fill a bucket faster than an operator notices.
const (
	uploadRateWindow = time.Minute
	uploadRateBurst  = 20
)

// Handler serves POST /api/v1/agent/attachments — the ONE endpoint through
// which a deported agent gets binary bytes into storage.
//
// It exists because the WS control channel is JSON: a PNG there would be
// base64 on the socket every agent uses to claim work. Agents hold no storage
// credentials by design, so the server is the only thing that can write the
// blob, and this is the narrow door it does it through.
type Handler struct {
	base.HandlerBase
	svc        *Service
	dbService  db.Service
	nonces     agents.NonceGuard
	authorizer *AuthorizerRegistry
	limiter    *agentRateLimiter
	logger     *slog.Logger
}

// NewHandler builds the agent attachment upload handler with the default
// authorizer set (today: incidents).
func NewHandler(svc *Service, dbService db.Service, cfg *config.Config) *Handler {
	registry := NewAuthorizerRegistry()
	registry.Register(EntityIncidents, NewIncidentAuthorizer(dbService))

	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         svc,
		dbService:   dbService,
		nonces:      agents.NewStoredNonceGuard(dbService, db.ErrAgentNonceReplayed),
		authorizer:  registry,
		limiter:     newAgentRateLimiter(uploadRateWindow, uploadRateBurst),
		logger:      slog.Default().With("component", "agent_attachments"),
	}
}

// SetNonceGuard swaps the replay guard. For tests and for embeddings with no
// `agent_nonces` table — same escape hatch agentws exposes.
func (h *Handler) SetNonceGuard(guard agents.NonceGuard) {
	h.nonces = guard
}

// Registry exposes the authorizer registry so a future attachment kind can
// register its own topic prefix without touching this handler.
func (h *Handler) Registry() *AuthorizerRegistry {
	return h.authorizer
}

// UploadResponse is the body returned on a successful upload. The agent
// references the file by uid from its result/diagnostics instead of carrying
// bytes anywhere else.
type UploadResponse struct {
	FileUID string `json:"fileUid"`
}

// Upload handles POST /api/v1/agent/attachments?topic=<topic>.
//
// The order of the checks below is deliberate and each one fails CLOSED:
// authenticate the agent, rate-limit it, validate the topic's SHAPE, ask the
// topic's authorizer who owns it (which is where the org comes from), and only
// then read and sniff the body.
func (h *Handler) Upload(writer http.ResponseWriter, req *http.Request) error {
	agent, authErr := h.authenticate(req)
	if authErr != nil {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeInvalidToken, authErr.Error())
	}

	if !h.limiter.allow(agent.UID, time.Now()) {
		return h.WriteError(writer, http.StatusTooManyRequests, base.ErrorCodeValidationError,
			"Too many attachment uploads from this agent")
	}

	topic, err := ParseTopic(req.URL.Query().Get("topic"))
	if err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error())
	}

	orgUID, err := h.authorizer.Authorize(req.Context(), topic, UploaderIdentity{
		AgentUID:    agent.UID,
		AgentOrgUID: agent.OrgUID(),
		AgentRegion: agent.Region,
	})
	if err != nil {
		// 403, not 404: the caller authenticated fine, it simply may not write
		// here. The message never says WHICH of the refusals fired — that
		// would turn the endpoint into an incident-existence oracle for any
		// enrolled agent.
		h.logger.WarnContext(req.Context(), "agent attachment upload refused",
			"agent", agent.UID, "topic", topic.Entity+"/"+topic.EntityUID+"/"+topic.Kind,
			"error", err)

		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden,
			"Not allowed to attach to this topic")
	}

	body, err := ReadCapped(req.Body)
	if err != nil {
		if errors.Is(err, ErrAttachmentTooLarge) {
			return h.WriteError(writer, http.StatusRequestEntityTooLarge,
				base.ErrorCodeValidationError, "Attachment exceeds the size limit")
		}

		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error())
	}

	fileUID, err := h.svc.Put(req.Context(), orgUID, rawTopic(topic), attachmentName(topic), body,
		models.JSONMap{
			DetailKeyTrigger: "agent-upload",
		})
	if err != nil {
		return h.writePutError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, UploadResponse{FileUID: fileUID})
}

// writePutError maps the service's refusals to statuses. Everything unmapped is
// a genuine 500 and is reported.
func (h *Handler) writePutError(writer http.ResponseWriter, req *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrEmptyAttachment):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Attachment body is empty")
	case errors.Is(err, ErrAttachmentTooLarge):
		return h.WriteError(writer, http.StatusRequestEntityTooLarge,
			base.ErrorCodeValidationError, "Attachment exceeds the size limit")
	case errors.Is(err, ErrUnsupportedMediaType):
		// Sniffed, not declared: a caller that labels a zip as image/png gets
		// here, which is the whole point of sniffing.
		return h.WriteError(writer, http.StatusUnsupportedMediaType,
			base.ErrorCodeValidationError, err.Error())
	default:
		return h.WriteInternalError(writer, req, err)
	}
}

func rawTopic(topic ParsedTopic) string {
	return topic.Entity + "/" + topic.EntityUID + "/" + topic.Kind
}

func attachmentName(topic ParsedTopic) string {
	return topic.Entity + "-" + topic.EntityUID + "-" + topic.Kind + ".png"
}

// errUnauthenticated is the single message every authentication failure
// produces. Distinguishing "unknown agent" from "bad signature" would leak
// which agent uids exist.
var errUnauthenticated = errors.New("Invalid agent signature")

// authenticate verifies the signed headers exactly the way the WS reconnect
// does: known active agent, timestamp inside the skew, nonce not seen before
// (cluster-wide, DB-backed), Ed25519 signature over method|path|timestamp|nonce.
func (h *Handler) authenticate(req *http.Request) (*models.Agent, error) {
	agentUID := req.Header.Get(headerAgentUID)
	timestamp := req.Header.Get(headerTimestamp)
	nonce := req.Header.Get(headerNonce)
	signature := req.Header.Get(headerSignature)

	if agentUID == "" || timestamp == "" || nonce == "" || signature == "" {
		return nil, errUnauthenticated
	}

	agent, err := h.dbService.GetAgent(req.Context(), agentUID)
	if err != nil || agent == nil {
		return nil, errUnauthenticated
	}

	if agent.Status != models.AgentStatusActive {
		return nil, errUnauthenticated
	}

	if tsErr := agents.ValidateTimestamp(timestamp, time.Now(), agents.DefaultClockSkew); tsErr != nil {
		return nil, errUnauthenticated
	}

	// Replay guard BEFORE the signature check, same order as agentws: a valid
	// signature presented twice must fail the second time, and the guard is
	// what makes that true. Fails closed — a store error rejects the upload.
	if nonceErr := h.nonces.CheckAndStore(
		req.Context(), agent.UID, nonce, time.Now(), nonceRetention,
	); nonceErr != nil {
		return nil, errUnauthenticated
	}

	if sigErr := agents.VerifySignature(
		agent.Ed25519PublicKey, req.Method, req.URL.Path, timestamp, nonce, signature,
	); sigErr != nil {
		return nil, errUnauthenticated
	}

	return agent, nil
}

// agentRateLimiter is a per-agent fixed-window counter. Fixed window rather
// than a token bucket because the quantity being bounded is "blobs written per
// minute", where the edge-of-window doubling a fixed window allows is
// irrelevant and the simplicity is worth more.
type agentRateLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	burst   int
	buckets map[string]*rateBucket
}

type rateBucket struct {
	windowStart time.Time
	count       int
}

func newAgentRateLimiter(window time.Duration, burst int) *agentRateLimiter {
	return &agentRateLimiter{
		window:  window,
		burst:   burst,
		buckets: make(map[string]*rateBucket),
	}
}

// allow reports whether this agent may upload now, and counts the attempt.
func (l *agentRateLimiter) allow(agentUID string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	bucket, ok := l.buckets[agentUID]
	if !ok || now.Sub(bucket.windowStart) >= l.window {
		l.buckets[agentUID] = &rateBucket{windowStart: now, count: 1}

		// Opportunistic sweep: the map is keyed by agent uid and would
		// otherwise grow with every agent that ever uploaded.
		l.sweepLocked(now)

		return true
	}

	if bucket.count >= l.burst {
		return false
	}

	bucket.count++

	return true
}

// sweepLocked drops buckets whose window has fully elapsed. Caller holds the lock.
func (l *agentRateLimiter) sweepLocked(now time.Time) {
	for uid, bucket := range l.buckets {
		if now.Sub(bucket.windowStart) >= l.window {
			delete(l.buckets, uid)
		}
	}
}
