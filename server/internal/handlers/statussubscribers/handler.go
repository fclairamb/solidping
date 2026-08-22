package statussubscribers

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/statuspagelock"
)

// Response/validation field names used more than once.
const (
	respKeyData    = "data"
	fieldURL       = "url"
	fieldChannel   = "channel"
	fieldBodyInput = "body"
)

// Handler provides HTTP handlers for subscriber management + the Atom feed.
type Handler struct {
	base.HandlerBase
	svc       *Service
	dbService db.Service
	sender    email.Sender
	formatter email.Formatter
	cfg       *config.Config
	logger    *slog.Logger
}

// NewHandler creates a new status subscribers handler.
func NewHandler(
	service *Service, dbService db.Service, sender email.Sender, formatter email.Formatter, cfg *config.Config,
) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
		dbService:   dbService,
		sender:      sender,
		formatter:   formatter,
		cfg:         cfg,
		logger:      slog.Default(),
	}
}

func (h *Handler) baseURL() string {
	if h.cfg != nil && h.cfg.Server.BaseURL != "" {
		return h.cfg.Server.BaseURL
	}

	return "http://localhost:4000"
}

// Subscribe handles POST /api/v1/orgs/:org/status-pages/:statusPageUid/subscribers.
// Public, no auth. Always returns 202 on success and sends a confirm mail.
func (h *Handler) Subscribe(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	statusPageUID := httpx.Param(req, "statusPageUid")

	var body SubscribeRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBodyInput, Message: "Invalid JSON format"},
		})
	}

	result, err := h.svc.Subscribe(req.Context(), orgSlug, statusPageUID, &body)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	h.sendConfirmMail(req.Context(), result)

	return h.WriteJSON(writer, http.StatusAccepted, map[string]any{
		respKeyData: map[string]any{
			"status": "pending_confirmation",
		},
	})
}

// sendConfirmMail renders and sends the double opt-in confirmation. Failure is
// logged but never surfaced to the public caller (no PII echoes).
func (h *Handler) sendConfirmMail(ctx context.Context, result *SubscribeResult) {
	if h.sender == nil || h.formatter == nil {
		return
	}

	data := &MailData{
		PageName:   result.PageName,
		ConfirmURL: confirmURL(h.baseURL(), result.Subscriber.ConfirmToken),
	}

	subject, htmlBody, textBody, err := h.formatter.Format("status-subscriber-confirm.html", map[string]any{
		"Subject":    MailKindConfirm.subject(data),
		"PageName":   data.PageName,
		"ConfirmURL": data.ConfirmURL,
	})
	if err != nil {
		h.logger.ErrorContext(ctx, "status subscriber confirm mail formatting failed",
			"subscriberUid", result.Subscriber.UID, "error", err)

		return
	}

	msg := &email.Message{
		Recipients: email.Recipients{To: []string{result.Subscriber.EmailAddress()}},
		Subject:    subject,
		HTML:       htmlBody,
		Text:       textBody,
	}

	if _, err := h.sender.Send(ctx, msg); err != nil {
		// Email is PII; log the opaque subscriber UID, never the address.
		h.logger.ErrorContext(ctx, "status subscriber confirm mail failed",
			"subscriberUid", result.Subscriber.UID, "error", err)
	}
}

// Confirm handles GET /api/v1/public/status-subscribers/confirm?token=… .
func (h *Handler) Confirm(writer http.ResponseWriter, req *http.Request) error {
	token := req.URL.Query().Get("token")

	result, err := h.svc.Confirm(req.Context(), token)
	if err != nil {
		return h.writeLandingError(writer, err)
	}

	return h.writeLanding(writer,
		"Subscription confirmed",
		fmt.Sprintf("You're now subscribed to updates for %s.", result.PageName),
		h.statusPageURL(result.OrgSlug, result.PageSlug))
}

// Unsubscribe handles GET /api/v1/public/status-subscribers/unsubscribe?token=… .
func (h *Handler) Unsubscribe(writer http.ResponseWriter, req *http.Request) error {
	token := req.URL.Query().Get("token")

	result, err := h.svc.Unsubscribe(req.Context(), token)
	if err != nil {
		return h.writeLandingError(writer, err)
	}

	return h.writeLanding(writer,
		"Unsubscribed",
		fmt.Sprintf("You will no longer receive updates for %s.", result.PageName),
		h.statusPageURL(result.OrgSlug, result.PageSlug))
}

func (h *Handler) statusPageURL(orgSlug, pageSlug string) string {
	if orgSlug == "" || pageSlug == "" {
		return ""
	}

	return fmt.Sprintf("%s/status0/%s/%s", h.baseURL(), orgSlug, pageSlug)
}

// CreateEndpointSubscriber handles
// POST /api/v1/orgs/:org/status-pages/:statusPageUid/subscribers (AUTHED).
//
// The public subscribe endpoint lives at a different path and is email-only.
// This one is deliberately operator-side: a visitor pasting an incoming-webhook
// URL has no verification story, and the URL is a credential that would then
// be accepted from anyone.
func (h *Handler) CreateEndpointSubscriber(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	statusPageUID := httpx.Param(req, "statusPageUid")

	var body CreateEndpointSubscriberRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBodyInput, Message: "Invalid JSON format"},
		})
	}

	created, err := h.svc.CreateEndpointSubscriber(req.Context(), orgSlug, statusPageUID, &body)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, map[string]any{respKeyData: created})
}

// ListSubscribers handles GET /api/v1/orgs/:org/status-pages/:statusPageUid/subscribers (authed).
func (h *Handler) ListSubscribers(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	statusPageUID := httpx.Param(req, "statusPageUid")

	subs, err := h.svc.ListSubscribers(req.Context(), orgSlug, statusPageUID)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{
		respKeyData: subs,
		"meta":      map[string]any{"count": len(subs)},
	})
}

// RemoveSubscriber handles DELETE /api/v1/orgs/:org/status-pages/:statusPageUid/subscribers/:uid (authed).
func (h *Handler) RemoveSubscriber(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	statusPageUID := httpx.Param(req, "statusPageUid")
	uid := httpx.Param(req, "uid")

	if err := h.svc.RemoveSubscriber(req.Context(), orgSlug, statusPageUID, uid); err != nil {
		return h.handleError(writer, req, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// handleError maps domain errors to HTTP responses (JSON).
func (h *Handler) handleError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	case errors.Is(err, ErrStatusPageNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeStatusPageNotFound, "Status page not found")
	case errors.Is(err, statuspagelock.ErrLocked):
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeStatusPageLocked,
			"This status page is password protected")
	case errors.Is(err, ErrSubscriberNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Subscriber not found")
	case errors.Is(err, ErrIncidentNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Incident not found")
	case errors.Is(err, ErrInvalidEmail):
		return h.WriteValidationError(writer, "Invalid email", []base.ValidationErrorField{
			{Name: "email", Message: "A valid email address is required"},
		})
	case errors.Is(err, ErrInvalidScope):
		return h.WriteValidationError(writer, "Invalid scope", []base.ValidationErrorField{
			{Name: "scope", Message: "Must be one of: page, incident"},
		})
	case errors.Is(err, ErrIncidentRequired):
		return h.WriteValidationError(writer, "Incident required", []base.ValidationErrorField{
			{Name: "incidentUid", Message: "incidentUid is required when scope is incident"},
		})
	case errors.Is(err, ErrInvalidChannel):
		return h.WriteValidationError(writer, "Invalid channel", []base.ValidationErrorField{
			{Name: fieldChannel, Message: "Must be one of: webhook, slack"},
		})
	case errors.Is(err, ErrPublicChannelNotAllowed):
		return h.WriteValidationError(writer, "Channel not available", []base.ValidationErrorField{
			{Name: fieldChannel, Message: "Public subscriptions are email-only. " +
				"Webhook and Slack deliveries are registered by an operator from the dashboard."},
		})
	case errors.Is(err, ErrEndpointRequired):
		return h.WriteValidationError(writer, "Endpoint required", []base.ValidationErrorField{
			{Name: fieldURL, Message: "A delivery URL is required"},
		})
	case errors.Is(err, ErrSlackWebhookExpected):
		return h.WriteValidationError(writer, "Slack webhook expected", []base.ValidationErrorField{
			{Name: fieldURL, Message: "A Slack incoming-webhook URL (https://hooks.slack.com/...) is required"},
		})
	case errors.Is(err, ErrInvalidEndpointURL):
		return h.WriteValidationError(writer, "Invalid endpoint URL", []base.ValidationErrorField{
			{Name: fieldURL, Message: "Must be an https URL pointing at a publicly reachable host"},
		})
	default:
		return h.WriteInternalError(writer, request, err)
	}
}

// writeLandingError renders an HTML landing page for token failures.
func (h *Handler) writeLandingError(writer http.ResponseWriter, err error) error {
	if errors.Is(err, ErrInvalidToken) {
		return h.writeLandingStatus(writer, http.StatusBadRequest,
			"Link expired",
			"This link is no longer valid. It may have already been used.", "")
	}

	return h.writeLandingStatus(writer, http.StatusInternalServerError,
		"Something went wrong",
		"Please try again later.", "")
}

func (h *Handler) writeLanding(writer http.ResponseWriter, title, body, pageURL string) error {
	return h.writeLandingStatus(writer, http.StatusOK, title, body, pageURL)
}

// writeLandingStatus renders a minimal, mobile-friendly HTML confirmation page.
func (h *Handler) writeLandingStatus(
	writer http.ResponseWriter, status int, title, body, pageURL string,
) error {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(status)

	link := ""
	if pageURL != "" {
		link = fmt.Sprintf(
			`<p><a href="%s" style="color:#2563eb;">Back to the status page</a></p>`,
			html.EscapeString(pageURL))
	}

	page := fmt.Sprintf(`<!DOCTYPE html><html lang="en"><head><meta charset="utf-8">`+
		`<meta name="viewport" content="width=device-width, initial-scale=1.0"><title>%s</title></head>`+
		`<body style="font-family:-apple-system,Segoe UI,Roboto,Arial,sans-serif;background:#f5f6f8;`+
		`margin:0;padding:48px 16px;color:#2d3748;text-align:center;">`+
		`<div style="max-width:480px;margin:0 auto;background:#fff;border-radius:8px;padding:32px;">`+
		`<h1 style="font-size:22px;color:#1a1a2e;">%s</h1><p>%s</p>%s</div></body></html>`,
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(body), link)

	_, err := writer.Write([]byte(page))

	return err
}

// --- Atom feed ---

// atomFeed is the root Atom document.
type atomFeed struct {
	XMLName  xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Title    string      `xml:"title"`
	ID       string      `xml:"id"`
	Updated  string      `xml:"updated"`
	Links    []atomLink  `xml:"link"`
	Entries  []atomEntry `xml:"entry"`
	Subtitle string      `xml:"subtitle,omitempty"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr,omitempty"`
}

type atomEntry struct {
	Title     string   `xml:"title"`
	ID        string   `xml:"id"`
	Updated   string   `xml:"updated"`
	Published string   `xml:"published"`
	Link      atomLink `xml:"link"`
	Content   atomText `xml:"content"`
}

type atomText struct {
	Type string `xml:"type,attr"`
	Body string `xml:",chardata"`
}

// Feed handles GET /api/v1/status-pages/:org/:slug/feed.xml — a public Atom feed
// of the status-update timeline. No auth.
func (h *Handler) Feed(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	slug := httpx.Param(req, "slug")

	ctx := req.Context()

	org, err := h.dbService.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return h.writeFeedNotFound(writer)
	}

	page, err := h.dbService.GetStatusPageBySlug(ctx, org.UID, slug)
	if err != nil || !statuspagelock.Visible(page) {
		return h.writeFeedNotFound(writer)
	}

	// The feed is a read of the page, so it is gated like the page. A feed
	// reader that followed the URL after unlocking in a browser carries the
	// cookie; one that never unlocked gets a 401 it can surface to its user.
	if !statuspagelock.RequestUnlocks(req, page) {
		return h.WriteError(writer, http.StatusUnauthorized,
			base.ErrorCodeStatusPageLocked, "This status page is password protected")
	}

	historyDays := page.HistoryDays
	if historyDays <= 0 {
		historyDays = 90
	}

	updates, err := h.dbService.ListPublicStatusUpdates(ctx, page.UID, historyDays)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	feed := h.buildFeed(orgSlug, slug, page.Name, updates)

	return h.writeFeed(writer, feed)
}

func (h *Handler) buildFeed(
	orgSlug, slug, pageName string, updates []*db.PublicStatusUpdate,
) *atomFeed {
	feedURL := fmt.Sprintf("%s/api/v1/status-pages/%s/%s/feed.xml", h.baseURL(), orgSlug, slug)
	pageURL := h.statusPageURL(orgSlug, slug)

	updated := time.Now().UTC().Format(time.RFC3339)
	if len(updates) > 0 {
		updated = updates[0].PublishedAt.UTC().Format(time.RFC3339)
	}

	feed := &atomFeed{
		Title:    pageName + " status updates",
		ID:       feedURL,
		Updated:  updated,
		Subtitle: "Status updates from " + pageName,
		Links: []atomLink{
			{Href: feedURL, Rel: "self"},
			{Href: pageURL, Rel: "alternate"},
		},
	}

	feed.Entries = make([]atomEntry, 0, len(updates))
	for _, upd := range updates {
		entryLink := pageURL
		if upd.LinkURL != nil && *upd.LinkURL != "" {
			entryLink = *upd.LinkURL
		}

		feed.Entries = append(feed.Entries, atomEntry{
			Title:     upd.Title,
			ID:        fmt.Sprintf("%s#%s", feedURL, upd.UID),
			Updated:   upd.PublishedAt.UTC().Format(time.RFC3339),
			Published: upd.PublishedAt.UTC().Format(time.RFC3339),
			Link:      atomLink{Href: entryLink},
			Content:   atomText{Type: "text", Body: upd.BodyMarkdown},
		})
	}

	return feed
}

func (h *Handler) writeFeed(writer http.ResponseWriter, feed *atomFeed) error {
	writer.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	writer.Header().Set("Cache-Control", "public, max-age=300")
	writer.WriteHeader(http.StatusOK)

	if _, err := writer.Write([]byte(xml.Header)); err != nil {
		return err
	}

	enc := xml.NewEncoder(writer)
	enc.Indent("", "  ")

	return enc.Encode(feed)
}

func (h *Handler) writeFeedNotFound(writer http.ResponseWriter) error {
	return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeStatusPageNotFound, "Status page not found")
}
