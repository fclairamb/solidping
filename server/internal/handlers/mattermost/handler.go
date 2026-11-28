// Package mattermost handles inbound Mattermost Blocks action callbacks.
//
// Unlike Slack and Discord, Mattermost incoming webhooks have no signing
// secret of their own. Authenticity therefore comes from the HMAC token we
// embed in props.mm_blocks_actions.context: Mattermost keeps that registry
// server-side (encrypted in the post's action cookie) and posts it back to us.
package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/incidentlinks"
	"github.com/fclairamb/solidping/server/internal/notifications"
)

// maxActionBody bounds the callback body. The Mattermost request is a small
// JSON object; anything larger is not a legitimate action.
const maxActionBody = 1 << 20

// store is the slice of the database the callback needs. Keeping it narrow
// makes the handler testable without a full database service.
type store interface {
	GetOrganization(ctx context.Context, uid string) (*models.Organization, error)
	GetChannel(ctx context.Context, uid string) (*models.Integration, error)
}

// incidentService is the incident operations the callback performs. It is
// satisfied by *incidents.Service.
type incidentService interface {
	GetIncidentByUID(ctx context.Context, orgUID, incidentUID string) (*models.Incident, error)
	GetCheckByUID(ctx context.Context, orgUID, checkUID string) (*models.Check, error)
	AcknowledgeIncidentFromMattermost(
		ctx context.Context, orgUID, incidentUID, mattermostUserID, mattermostUsername, integrationUID string,
	) (*models.Incident, error)
}

// Handler serves POST /api/v1/integrations/mattermost/actions.
type Handler struct {
	base.HandlerBase
	db        store
	incidents incidentService
	cfg       *config.Config
}

// NewHandler creates the Mattermost action handler.
func NewHandler(dbService store, incidentsService incidentService, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		db:          dbService,
		incidents:   incidentsService,
		cfg:         cfg,
	}
}

// postActionRequest is the Mattermost PostActionIntegrationRequest shape.
// Context carries the token and action ID we put in mm_blocks_actions; for a
// static_select it also carries selected_option.
type postActionRequest struct {
	UserID      string         `json:"user_id"`      //nolint:tagliatelle // Mattermost wire format
	UserName    string         `json:"user_name"`    //nolint:tagliatelle // Mattermost wire format
	ChannelID   string         `json:"channel_id"`   //nolint:tagliatelle // Mattermost wire format
	ChannelName string         `json:"channel_name"` //nolint:tagliatelle // Mattermost wire format
	TeamID      string         `json:"team_id"`      //nolint:tagliatelle // Mattermost wire format
	TeamDomain  string         `json:"team_domain"`  //nolint:tagliatelle // Mattermost wire format
	PostID      string         `json:"post_id"`      //nolint:tagliatelle // Mattermost wire format
	TriggerID   string         `json:"trigger_id"`   //nolint:tagliatelle // Mattermost wire format
	Type        string         `json:"type"`
	DataSource  string         `json:"data_source"` //nolint:tagliatelle // Mattermost wire format
	Context     map[string]any `json:"context"`
}

// postActionResponse is the subset of PostActionIntegrationResponse we use.
// Updates replace the original post; ephemeral_text is a private message to
// the person who clicked. Mattermost has no separate error field in the model
// this server version follows, so expected failures are ephemeral text too.
type postActionResponse struct {
	Update        *postUpdate `json:"update,omitempty"`
	EphemeralText string      `json:"ephemeral_text,omitempty"` //nolint:tagliatelle // Mattermost wire format
}

type postUpdate struct {
	Message string                        `json:"message,omitempty"`
	Props   notifications.MattermostProps `json:"props"`
}

// HandleAction verifies the action token, resolves the incident and dispatches
// one acknowledge. It always answers 200 with a Mattermost action
// response so the reader sees a useful private message instead of a generic
// network error.
func (h *Handler) HandleAction(writer http.ResponseWriter, req *http.Request) error {
	if h.cfg.Auth.JWTSecret == "" {
		slog.ErrorContext(req.Context(), "Mattermost action received but JWT secret is not configured")

		return h.respond(writer, nil, "Mattermost actions are not configured on this instance.")
	}

	req.Body = http.MaxBytesReader(writer, req.Body, maxActionBody)

	var body postActionRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		return h.respond(writer, nil, "Invalid action request.")
	}

	// The callback is authenticated only by the signed action token, not the
	// user fields: cap them before they reach the ack trail and the card.
	body.UserID = capActionUserField(body.UserID)
	body.UserName = capActionUserField(body.UserName)

	token, _ := body.Context["token"].(string)
	action, _ := body.Context["action"].(string)
	if token == "" || action == "" {
		return h.respond(writer, nil, "Invalid action request.")
	}

	payload, err := incidentlinks.VerifyMattermostAction([]byte(h.cfg.Auth.JWTSecret), token)
	if err != nil {
		slog.WarnContext(req.Context(), "Rejected Mattermost action token", "error", err)

		return h.respond(writer, nil, "This action link is invalid or has expired. Open the incident in SolidPing.")
	}

	org, connection, incident, ok := h.resolve(req.Context(), payload, writer)
	if !ok {
		return nil
	}

	if err := payload.Validate(org.UID, incident.UID, connection.UID, action); err != nil {
		slog.WarnContext(req.Context(), "Rejected Mattermost action context",
			"error", err, "incidentUid", incident.UID, "integrationUid", connection.UID)

		return h.respond(writer, nil, "This action is not valid for this incident.")
	}

	if incident.State == models.IncidentStateResolved {
		return h.respond(writer, nil, "This incident is already resolved. Open it in SolidPing for details.")
	}

	switch action {
	case notifications.MattermostActionAcknowledge:
		return h.handleAcknowledge(writer, req, &body, org, connection, incident)
	default:
		slog.WarnContext(req.Context(), "Unknown Mattermost action", "action", action)

		return h.respond(writer, nil, "This action is not supported.")
	}
}

// resolve loads the org, connection and incident named by the token. Every
// failure is a private message, never a 5xx: the click is a user action on a
// possibly stale post, not a server fault.
func (h *Handler) resolve(
	ctx context.Context, payload *incidentlinks.MattermostActionTokenPayload, writer http.ResponseWriter,
) (*models.Organization, *models.Integration, *models.Incident, bool) {
	org, err := h.db.GetOrganization(ctx, payload.OrgUID)
	if err != nil || org == nil {
		_ = h.respond(writer, nil, "This organization no longer exists.")

		return nil, nil, nil, false
	}

	connection, err := h.db.GetChannel(ctx, payload.IntegrationUID)
	if err != nil || connection == nil ||
		connection.Type != models.ConnectionTypeMattermost ||
		connection.OrganizationUID != org.UID {
		_ = h.respond(writer, nil, "This Mattermost integration no longer exists.")

		return nil, nil, nil, false
	}

	if !connection.Enabled {
		_ = h.respond(writer, nil, "This Mattermost integration is disabled.")

		return nil, nil, nil, false
	}

	incident, err := h.incidents.GetIncidentByUID(ctx, org.UID, payload.IncidentUID)
	if err != nil || incident == nil {
		_ = h.respond(writer, nil, "This incident no longer exists.")

		return nil, nil, nil, false
	}

	return org, connection, incident, true
}

func (h *Handler) handleAcknowledge(
	writer http.ResponseWriter, req *http.Request, body *postActionRequest,
	org *models.Organization, connection *models.Integration, incident *models.Incident,
) error {
	if incident.AcknowledgedAt != nil {
		return h.respond(writer, nil, "This incident is already acknowledged.")
	}

	updated, err := h.incidents.AcknowledgeIncidentFromMattermost(
		req.Context(), org.UID, incident.UID, body.UserID, body.UserName, connection.UID,
	)
	if err != nil {
		slog.ErrorContext(req.Context(), "Failed to acknowledge incident from Mattermost",
			"error", err, "incidentUid", incident.UID)

		return h.respond(writer, nil, "Could not acknowledge this incident.")
	}

	check := h.loadCheck(req.Context(), org.UID, updated.CheckUID)
	payload := h.notificationPayload(org, connection, updated, check, string(models.EventTypeIncidentAcknowledged))
	payload.Acknowledgment = &notifications.AckInfo{ActorName: mattermostActor(body.UserName), Via: "mattermost"}

	post := notifications.BuildMattermostBlocksPost(payload, h.renderOptions())

	return h.respond(writer, &postUpdate{Props: post.Props},
		"Acknowledged by "+mattermostMention(body.UserName)+".")
}

// notificationPayload is the minimum the block renderer needs for an in-place
// status card. EventKey stays empty on purpose: these are not notification
// jobs, and the renderer takes its actions from opts, not from EventKey.
func (h *Handler) notificationPayload(
	org *models.Organization, connection *models.Integration,
	incident *models.Incident, check *models.Check, eventType string,
) *notifications.Payload {
	return &notifications.Payload{
		EventType:   eventType,
		Incident:    incident,
		Check:       check,
		Integration: connection,
		OrgSlug:     org.Slug,
		AppBaseURL:  h.cfg.Server.BaseURL,
	}
}

// renderOptions sets the public callback URL. The acknowledgment card keeps
// the Open incident action; no signed token is needed for an openURL.
func (h *Handler) renderOptions() *notifications.MattermostBlocksRenderOptions {
	return &notifications.MattermostBlocksRenderOptions{
		ActionURL:     notifications.MattermostActionURL(h.cfg.Server.BaseURL),
		EnableActions: true,
	}
}

// loadCheck tolerates a deleted check: the incident and its dashboard page
// still exist, and the card can say "Unknown check" instead of failing the
// acknowledgment that already happened.
func (h *Handler) loadCheck(ctx context.Context, orgUID, checkUID string) *models.Check {
	check, err := h.incidents.GetCheckByUID(ctx, orgUID, checkUID)
	if err == nil && check != nil {
		return check
	}

	name := "Unknown check"

	return &models.Check{UID: checkUID, Name: &name}
}

// respond writes the Mattermost action response. An update is optional so a
// stale or invalid click only shows the private message and leaves the post
// untouched.
func (h *Handler) respond(
	writer http.ResponseWriter, update *postUpdate, ephemeral string,
) error {
	return h.WriteJSON(writer, http.StatusOK, postActionResponse{
		Update:        update,
		EphemeralText: ephemeral,
	})
}

// maxActionUserField is the most runes kept from the request's user_id and
// user_name. A genuine Mattermost username is at most 22 characters and an ID
// 26, so this only trims forged values.
const maxActionUserField = 64

// capActionUserField trims a user field and keeps its first
// maxActionUserField runes.
func capActionUserField(value string) string {
	value = strings.TrimSpace(value)

	if utf8.RuneCountInString(value) > maxActionUserField {
		value = strings.TrimSpace(string([]rune(value)[:maxActionUserField]))
	}

	return value
}

// mattermostActor is the acknowledgment's actor name, kept raw: the ack event
// stores it as data, and the card renderer escapes it as markdown.
func mattermostActor(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "Someone"
	}

	return name
}

func mattermostMention(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "someone"
	}

	// The ephemeral text renders as markdown. A genuine username (letters,
	// digits, `.`, `_`, `-`) is a mention as is; anything else is escaped so
	// it cannot add a link or formatting.
	if mattermostUsernamePattern.MatchString(name) {
		return "@" + name
	}

	return "@" + notifications.EscapeMattermostMarkdown(name)
}

// mattermostUsernamePattern is the character set Mattermost allows in a
// username.
var mattermostUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
