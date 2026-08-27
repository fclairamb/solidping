// Package supportinbox exposes the instance support inbox over HTTP.
//
// Every route here is SuperAdmin-gated. The dash0 route that consumes it is
// unlinked — reachable only by typing the URL — but that is discoverability, not
// access control: the URL must be treated as public knowledge and the gate below
// is the only thing that actually protects the data (spec 2026-08-22-02).
//
// There is deliberately NO org-scoped variant of any of this in v1. Threads
// carry an organization/user attribution so an org-facing view stays possible
// later, but attribution is a hint for the operator, never a boundary that
// grants an org visibility of a thread.
package supportinbox

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/support"
)

// Handler serves /api/v1/support.
type Handler struct {
	base.HandlerBase
	svc *support.Service
}

// NewHandler builds the support inbox handler.
func NewHandler(svc *support.Service, cfg *config.Config) *Handler {
	return &Handler{HandlerBase: base.NewHandlerBase(cfg), svc: svc}
}

// ThreadResponse is the API shape of a support thread.
type ThreadResponse struct {
	UID             string                    `json:"uid"`
	Channel         string                    `json:"channel"`
	ChannelIdentity string                    `json:"channelIdentity"`
	Subject         string                    `json:"subject"`
	Status          string                    `json:"status"`
	OrganizationUID *string                   `json:"organizationUid,omitempty"`
	UserUID         *string                   `json:"userUid,omitempty"`
	LastMessageAt   time.Time                 `json:"lastMessageAt"`
	LastInboundAt   *time.Time                `json:"lastInboundAt,omitempty"`
	UnreadCount     int                       `json:"unreadCount"`
	CreatedAt       time.Time                 `json:"createdAt"`
	UpdatedAt       time.Time                 `json:"updatedAt"`
	ReplyWindow     models.SupportReplyWindow `json:"replyWindow"`
	CanReply        bool                      `json:"canReply"`
	// CanReplyReason explains a false CanReply in operator-facing terms, so the
	// dashboard can say WHY the reply box is disabled instead of showing a
	// generic "no adapter" that is wrong for most of the cases that produce it.
	CanReplyReason string `json:"canReplyReason,omitempty"`
}

// MessageResponse is the API shape of a support message.
type MessageResponse struct {
	UID        string         `json:"uid"`
	ThreadUID  string         `json:"threadUid"`
	Channel    string         `json:"channel"`
	Direction  string         `json:"direction"`
	Body       string         `json:"body"`
	Truncated  bool           `json:"truncated"`
	RawType    string         `json:"rawType"`
	ExternalID *string        `json:"externalId,omitempty"`
	AuthorUID  *string        `json:"authorUid,omitempty"`
	Delivery   map[string]any `json:"delivery,omitempty"`
	CreatedAt  time.Time      `json:"createdAt"`
}

// UpdateThreadRequest is the PATCH body.
type UpdateThreadRequest struct {
	Status  *string `json:"status,omitempty"`
	Subject *string `json:"subject,omitempty"`
}

// CreateMessageRequest is the reply body.
type CreateMessageRequest struct {
	Body string `json:"body"`
}

// toThread renders one thread, running the per-thread reply pre-flight.
func (h *Handler) toThread(ctx context.Context, thread *models.SupportThread) *ThreadResponse {
	return toThreadWithRoute(thread, h.svc.ReplyRouteFor(ctx, thread))
}

// toThreadWithRoute renders a thread against an already-computed pre-flight, so
// a listing can memoize the routing lookups instead of repeating one per row.
func toThreadWithRoute(thread *models.SupportThread, route support.ReplyRoute) *ThreadResponse {
	return &ThreadResponse{
		UID:             thread.UID,
		Channel:         thread.Channel,
		ChannelIdentity: thread.ChannelIdentity,
		Subject:         thread.Subject,
		Status:          thread.Status,
		OrganizationUID: thread.OrganizationUID,
		UserUID:         thread.UserUID,
		LastMessageAt:   thread.LastMessageAt,
		LastInboundAt:   thread.LastInboundAt,
		UnreadCount:     thread.UnreadCount,
		CreatedAt:       thread.CreatedAt,
		UpdatedAt:       thread.UpdatedAt,
		// Derived at read time, never stored, so it cannot go stale.
		ReplyWindow: thread.ReplyWindow(time.Now()),
		// PER THREAD, not per channel: a Slack workspace with no stored
		// connection reports false here even though the Slack adapter is
		// registered instance-wide.
		CanReply:       route.CanReply,
		CanReplyReason: route.Reason,
	}
}

func toMessage(msg *models.SupportMessage) *MessageResponse {
	return &MessageResponse{
		UID:        msg.UID,
		ThreadUID:  msg.ThreadUID,
		Channel:    msg.Channel,
		Direction:  msg.Direction,
		Body:       msg.Body,
		Truncated:  msg.Truncated,
		RawType:    msg.RawType,
		ExternalID: msg.ExternalID,
		AuthorUID:  msg.AuthorUID,
		Delivery:   msg.Delivery,
		CreatedAt:  msg.CreatedAt,
	}
}

// ListThreads handles GET /api/v1/support/threads.
func (h *Handler) ListThreads(writer http.ResponseWriter, req *http.Request) error {
	query := req.URL.Query()

	filter := models.ListSupportThreadsFilter{
		Status:  query.Get("status"),
		Channel: query.Get("channel"),
		Query:   query.Get("q"),
	}

	if raw := query.Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			filter.Limit = parsed
		}
	}

	if filter.Status != "" && !models.ValidSupportStatus(filter.Status) {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Unknown status filter")
	}

	if filter.Channel != "" && !models.ValidSupportChannel(filter.Channel) {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Unknown channel filter")
	}

	threads, err := h.svc.ListThreads(req.Context(), filter)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	routes := h.svc.ReplyRoutes(req.Context(), threads)

	out := make([]*ThreadResponse, 0, len(threads))
	for _, thread := range threads {
		out = append(out, toThreadWithRoute(thread, routes[thread.UID]))
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{"data": out})
}

// GetThread handles GET /api/v1/support/threads/:uid.
func (h *Handler) GetThread(writer http.ResponseWriter, req *http.Request) error {
	thread, err := h.svc.GetThread(req.Context(), httpx.Param(req, "uid"))
	if err != nil {
		return h.threadError(writer, req, err)
	}

	// Opening a thread is reading it. Best-effort: a failed counter reset must
	// not stop an operator from seeing the conversation.
	if thread.UnreadCount > 0 {
		_ = h.svc.MarkRead(req.Context(), thread.UID)

		thread.UnreadCount = 0
	}

	return h.WriteJSON(writer, http.StatusOK, h.toThread(req.Context(), thread))
}

// UpdateThread handles PATCH /api/v1/support/threads/:uid.
func (h *Handler) UpdateThread(writer http.ResponseWriter, req *http.Request) error {
	var body UpdateThreadRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteErrorErr(writer, req, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Invalid request body", err)
	}

	if body.Status != nil && !models.ValidSupportStatus(*body.Status) {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Status must be open, pending or closed")
	}

	thread, err := h.svc.UpdateThread(req.Context(), httpx.Param(req, "uid"), body.Status, body.Subject)
	if err != nil {
		return h.threadError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, h.toThread(req.Context(), thread))
}

// ListMessages handles GET /api/v1/support/threads/:uid/messages.
func (h *Handler) ListMessages(writer http.ResponseWriter, req *http.Request) error {
	uid := httpx.Param(req, "uid")

	if _, err := h.svc.GetThread(req.Context(), uid); err != nil {
		return h.threadError(writer, req, err)
	}

	limit := 0
	if raw := req.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}

	messages, err := h.svc.ListMessages(req.Context(), uid, limit)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	out := make([]*MessageResponse, 0, len(messages))
	for _, msg := range messages {
		out = append(out, toMessage(msg))
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{"data": out})
}

// CreateMessage handles POST /api/v1/support/threads/:uid/messages — it sends
// the reply through the originating channel AND records it.
func (h *Handler) CreateMessage(writer http.ResponseWriter, req *http.Request) error {
	var body CreateMessageRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteErrorErr(writer, req, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Invalid request body", err)
	}

	authorUID := ""
	if user, ok := middleware.GetUserFromContext(req.Context()); ok {
		authorUID = user.UID
	}

	msg, err := h.svc.Reply(req.Context(), httpx.Param(req, "uid"), body.Body, authorUID)

	switch {
	case errors.Is(err, support.ErrEmptyReply):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Reply body cannot be empty")
	case errors.Is(err, support.ErrReplyWindowClosed):
		// 409, not 500: the channel refused, and the reason is actionable. The
		// UI already disables the box, so reaching this means the window lapsed
		// between render and send.
		return h.WriteErrorErr(writer, req, http.StatusConflict, base.ErrorCodeConflict,
			err.Error(), err)
	case errors.Is(err, support.ErrNoReplier):
		return h.WriteErrorErr(writer, req, http.StatusConflict, base.ErrorCodeConflict,
			"This channel cannot be replied to from the support inbox", err)
	case errors.Is(err, support.ErrNoReplyRoute):
		// 409 and NOTHING STORED. The dashboard already disables the box from
		// the same pre-flight, so reaching here means a stale tab, a scripted
		// caller, or a connection that disappeared between render and send —
		// and in none of those cases was a send attempted, so recording a
		// failed delivery would be inventing an event that never happened.
		return h.WriteErrorErr(writer, req, http.StatusConflict, base.ErrorCodeConflict,
			err.Error(), err)
	case err != nil && msg != nil:
		// The send failed but the attempt IS recorded, delivery status and all.
		// Returning the row rather than a bare error is what stops an operator
		// from answering the same person twice.
		return h.WriteJSON(writer, http.StatusAccepted, toMessage(msg))
	case err != nil:
		return h.threadError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, toMessage(msg))
}

// ResendMessage handles POST /api/v1/support/threads/:uid/messages/:messageUid/resend.
//
// It re-runs the full pre-flight and the send for an outbound reply whose
// delivery failed, rewriting that row rather than appending a second copy. This
// is the operator's way out of text that is visibly stored and permanently
// unsent — including every reply queued against a Slack workspace before it was
// connected.
func (h *Handler) ResendMessage(writer http.ResponseWriter, req *http.Request) error {
	msg, err := h.svc.Resend(req.Context(), httpx.Param(req, "uid"), httpx.Param(req, "messageUid"))

	switch {
	case errors.Is(err, support.ErrMessageNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound,
			"Support message not found")
	case errors.Is(err, support.ErrNotResendable):
		return h.WriteErrorErr(writer, req, http.StatusConflict, base.ErrorCodeConflict,
			"Only an outbound reply whose delivery failed can be resent", err)
	case errors.Is(err, support.ErrReplyWindowClosed),
		errors.Is(err, support.ErrNoReplier),
		errors.Is(err, support.ErrNoReplyRoute):
		return h.WriteErrorErr(writer, req, http.StatusConflict, base.ErrorCodeConflict,
			err.Error(), err)
	case err != nil && msg != nil:
		// Attempted and rejected again: the row's delivery record is updated
		// with the new provider error, and the operator sees the fresh reason
		// rather than the stale one.
		return h.WriteJSON(writer, http.StatusAccepted, toMessage(msg))
	case err != nil:
		return h.threadError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, toMessage(msg))
}

// threadError maps service errors onto the repo's error shape.
func (h *Handler) threadError(writer http.ResponseWriter, req *http.Request, err error) error {
	if errors.Is(err, support.ErrThreadNotFound) {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Support thread not found")
	}

	return h.WriteInternalError(writer, req, err)
}
