// Package usernotifications provides HTTP handlers and business logic for
// per-user notification contact management and route configuration.
package usernotifications

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
	"github.com/fclairamb/solidping/server/internal/integrations/whatsapp"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

// Service errors.
var (
	ErrOrgNotFound              = errors.New("organization not found")
	ErrRouteNotFound            = errors.New("notification route not found")
	ErrContactNotFound          = errors.New("notification contact not found")
	ErrUserNotFound             = errors.New("user not found")
	ErrRouteNotFoundAfterCreate = errors.New("route not found after creation")
	ErrEmailSenderNotConfigured = errors.New("email sender not configured")
	ErrNoSlackChannelForOrg     = errors.New("no Slack channel configured for this organization")
	ErrSlackClientNotConfigured = errors.New("slack client not configured")
	ErrWebPushNotConfigured     = errors.New("web push not configured on this server")
	// ErrInvalidWhatsAppNumber is returned when a WhatsApp contact value is not
	// a syntactically valid E.164 number.
	ErrInvalidWhatsAppNumber = errors.New("WhatsApp number must be in E.164 format (e.g. +15551234567)")
)

// ContactResponse is the API representation of a UserContact.
type ContactResponse struct {
	UID        string     `json:"uid"`
	Type       string     `json:"type"`
	Value      string     `json:"value"`
	Label      string     `json:"label"`
	VerifiedAt *time.Time `json:"verifiedAt,omitempty"`
}

// RouteResponse is the API representation of a UserNotificationRoute.
type RouteResponse struct {
	UID       string          `json:"uid"`
	Enabled   bool            `json:"enabled"`
	Position  int             `json:"position"`
	Contact   ContactResponse `json:"contact"`
	CreatedAt time.Time       `json:"createdAt"`
}

// SlackSuggestion carries the Slack DM hint when the user has a Slack provider
// and the org has a Slack channel, but no Slack DM contact yet.
type SlackSuggestion struct {
	SlackUserID   string `json:"slackUserId"`
	WorkspaceName string `json:"workspaceName"`
	ChannelUID    string `json:"channelUid"`
}

// ListRoutesResponse wraps the list response with the optional Slack suggestion.
type ListRoutesResponse struct {
	Data            []*RouteResponse `json:"data"`
	SlackSuggestion *SlackSuggestion `json:"slackSuggestion,omitempty"`
}

// CreateContactRequest is the body for POST /notification-contacts.
type CreateContactRequest struct {
	Type  string `json:"type"`
	Value string `json:"value"`
	Label string `json:"label"`
}

// PatchRouteRequest is the body for PATCH /notification-routes/:routeUid.
type PatchRouteRequest struct {
	Enabled   *bool    `json:"enabled,omitempty"`
	RouteUIDs []string `json:"routeUids,omitempty"` // full ordered list for reorder
}

// Service provides business logic for the usernotifications domain.
type Service struct {
	db db.Service
	// creds decrypts the org's Twilio auth token when sending a phone
	// verification code. May be nil in tests that don't exercise phone verify.
	creds credentials.Service
	// clock is injectable for deterministic verification-code expiry in tests.
	clock func() time.Time
	// whatsAppCfg is the instance-level WhatsApp configuration used to build
	// the production verification-code sender. Zero value = feature off.
	whatsAppCfg config.WhatsAppConfig
	// whatsAppSender overrides the config-derived sender. Injected per service
	// instance — deliberately NOT a package-level seam, so parallel tests can
	// never race on it.
	whatsAppSender WhatsAppCodeSender
	// telegramCfg is the instance-level Telegram configuration used to mint
	// connect links. Zero value = feature off.
	telegramCfg config.TelegramConfig
}

// Option customizes a Service at construction.
type Option func(*Service)

// WithWhatsAppConfig supplies the instance WhatsApp configuration.
func WithWhatsAppConfig(cfg *config.WhatsAppConfig) Option {
	return func(s *Service) {
		if cfg != nil {
			s.whatsAppCfg = *cfg
		}
	}
}

// WithWhatsAppSender injects an explicit WhatsApp verification-code sender,
// taking precedence over the config-derived one.
func WithWhatsAppSender(sender WhatsAppCodeSender) Option {
	return func(s *Service) { s.whatsAppSender = sender }
}

// NewService builds a service. creds may be nil when the phone verification
// flow is not exercised (e.g. unit tests of the route/contact CRUD paths).
func NewService(dbService db.Service, creds credentials.Service, opts ...Option) *Service {
	svc := &Service{db: dbService, creds: creds, clock: time.Now}
	for _, opt := range opts {
		opt(svc)
	}

	return svc
}

// now returns the service clock, defaulting to time.Now when unset.
func (s *Service) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}

	return time.Now()
}

// resolveOrgUID maps an org slug to its UID.
func (s *Service) resolveOrgUID(ctx context.Context, orgSlug string) (string, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil || org == nil {
		return "", ErrOrgNotFound
	}

	return org.UID, nil
}

// ListRoutes seeds the default email route then returns all routes + Slack suggestion.
func (s *Service) ListRoutes(
	ctx context.Context, orgSlug string, user *models.User,
) (*ListRoutesResponse, error) {
	orgUID, err := s.resolveOrgUID(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	// Auto-seed default email route on first visit.
	if seedErr := s.db.EnsureDefaultEmailRoute(ctx, user.UID, orgUID, user.Email); seedErr != nil {
		return nil, fmt.Errorf("seed default email route: %w", seedErr)
	}

	routes, err := s.db.ListUserContactsWithRoutes(ctx, user.UID, orgUID)
	if err != nil {
		return nil, fmt.Errorf("list routes: %w", err)
	}

	resp := &ListRoutesResponse{
		Data: toRouteResponses(routes),
	}

	resp.SlackSuggestion = s.buildSlackSuggestion(ctx, user, orgUID, routes)

	return resp, nil
}

// buildSlackSuggestion returns a suggestion if all conditions are met.
func (s *Service) buildSlackSuggestion(
	ctx context.Context, user *models.User, orgUID string,
	existing []*models.UserNotificationRoute,
) *SlackSuggestion {
	// Does the user have a Slack provider?
	providers, err := s.db.ListUserProvidersByUser(ctx, user.UID)
	if err != nil {
		return nil
	}

	var slackProvider *models.UserProvider

	for _, p := range providers {
		if p.ProviderType == models.ProviderTypeSlack {
			slackProvider = p

			break
		}
	}

	if slackProvider == nil {
		return nil
	}

	// Does the org have a Slack channel?
	slackChannel, err := s.db.GetSlackChannelForOrg(ctx, orgUID)
	if err != nil {
		return nil
	}

	// Is there already a slack_user contact for this provider ID?
	for _, route := range existing {
		if route.Contact != nil &&
			route.Contact.Type == models.UserContactTypeSlackUser &&
			route.Contact.Value == slackProvider.ProviderID {
			return nil // already added
		}
	}

	settings, err := models.SlackSettingsFromJSONMap(slackChannel.Settings)
	if err != nil {
		return nil
	}

	return &SlackSuggestion{
		SlackUserID:   slackProvider.ProviderID,
		WorkspaceName: settings.TeamName,
		ChannelUID:    slackChannel.UID,
	}
}

// CreateContact creates a new contact + route.
//
//nolint:cyclop // inherent complexity: upsert + reload + conditional route creation
func (s *Service) CreateContact(
	ctx context.Context, orgSlug string, user *models.User, req CreateContactRequest,
) (*RouteResponse, error) {
	// Telegram contacts are NEVER created here. Their value is a chat id and
	// there is no verification round-trip that could catch a wrong one, so
	// accepting one from a request body would let any user page a stranger.
	// The only way to create one is the connect flow: the user presses Start in
	// Telegram, and the webhook creates the contact from the chat that actually
	// sent the /start.
	if req.Type == models.UserContactTypeTelegram {
		return nil, ErrTelegramContactNotDirect
	}

	orgUID, err := s.resolveOrgUID(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	// Find the current max position.
	existing, listErr := s.db.ListUserContactsWithRoutes(ctx, user.UID, orgUID)
	if listErr != nil {
		return nil, fmt.Errorf("list existing routes: %w", listErr)
	}

	position := len(existing)

	// WhatsApp destinations must be E.164 — Meta rejects anything else, and a
	// malformed number would otherwise sit in the list looking verifiable.
	if req.Type == models.UserContactTypeWhatsApp && !whatsapp.ValidE164(strings.TrimSpace(req.Value)) {
		return nil, ErrInvalidWhatsAppNumber
	}

	contact := models.NewUserContact(user.UID, orgUID, req.Type, req.Value, req.Label)

	if req.Type == models.UserContactTypeEmail || req.Type == models.UserContactTypeWebPush {
		// Email is verified by sending; web push is verified by subscribing
		// (browser grants permission and registers the subscription endpoint).
		now := time.Now()
		contact.VerifiedAt = &now
	}

	if upsertErr := s.db.UpsertUserContact(ctx, contact); upsertErr != nil {
		return nil, fmt.Errorf("create contact: %w", upsertErr)
	}

	// Reload to get the actual UID after upsert.
	routes, reloadErr := s.db.ListUserContactsWithRoutes(ctx, user.UID, orgUID)
	if reloadErr != nil {
		return nil, fmt.Errorf("reload routes: %w", reloadErr)
	}

	// Find the route for our new contact (may already exist after upsert).
	for _, r := range routes {
		if r.Contact != nil && r.Contact.Type == req.Type && r.Contact.Value == req.Value {
			return toRouteResponse(r), nil
		}
	}

	// No route yet — create it.
	route := models.NewUserNotificationRoute(user.UID, orgUID, contact.UID, position)
	if _, insertErr := s.db.DB().NewInsert().Model(route).
		On("CONFLICT (contact_uid) DO NOTHING").
		Exec(ctx); insertErr != nil {
		return nil, fmt.Errorf("create route: %w", insertErr)
	}

	// Reload again.
	routes, reloadErr = s.db.ListUserContactsWithRoutes(ctx, user.UID, orgUID)
	if reloadErr != nil {
		return nil, fmt.Errorf("reload routes after create: %w", reloadErr)
	}

	for _, r := range routes {
		if r.Contact != nil && r.Contact.Type == req.Type && r.Contact.Value == req.Value {
			return toRouteResponse(r), nil
		}
	}

	return nil, ErrRouteNotFoundAfterCreate
}

// PatchRoute updates enabled flag and/or reorders.
func (s *Service) PatchRoute(
	ctx context.Context, orgSlug string, user *models.User,
	routeUID string, req PatchRouteRequest,
) (*RouteResponse, error) {
	orgUID, err := s.resolveOrgUID(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	if req.Enabled != nil {
		if enableErr := s.db.SetRouteEnabled(ctx, routeUID, *req.Enabled); enableErr != nil {
			return nil, fmt.Errorf("set route enabled: %w", enableErr)
		}
	}

	if len(req.RouteUIDs) > 0 {
		if reorderErr := s.db.ReorderRoutes(ctx, user.UID, orgUID, req.RouteUIDs); reorderErr != nil {
			return nil, fmt.Errorf("reorder routes: %w", reorderErr)
		}
	}

	// Return the updated route.
	routes, err := s.db.ListUserContactsWithRoutes(ctx, user.UID, orgUID)
	if err != nil {
		return nil, fmt.Errorf("reload routes: %w", err)
	}

	for _, r := range routes {
		if r.UID == routeUID {
			return toRouteResponse(r), nil
		}
	}

	return nil, ErrRouteNotFound
}

// DeleteContact soft-deletes a contact (the route cascades via the DB constraint).
func (s *Service) DeleteContact(
	ctx context.Context, orgSlug string, user *models.User, contactUID string,
) error {
	orgUID, err := s.resolveOrgUID(ctx, orgSlug)
	if err != nil {
		return err
	}

	// Verify the contact belongs to this user + org before deleting.
	routes, err := s.db.ListUserContactsWithRoutes(ctx, user.UID, orgUID)
	if err != nil {
		return fmt.Errorf("list routes for ownership check: %w", err)
	}

	found := false

	for _, r := range routes {
		if r.Contact != nil && r.Contact.UID == contactUID {
			found = true

			break
		}
	}

	if !found {
		return ErrContactNotFound
	}

	return s.db.DeleteUserContact(ctx, contactUID)
}

// toRouteResponse converts a DB model to an API response.
func toRouteResponse(route *models.UserNotificationRoute) *RouteResponse {
	contactResp := ContactResponse{}
	if route.Contact != nil {
		contactResp = ContactResponse{
			UID:        route.Contact.UID,
			Type:       route.Contact.Type,
			Value:      route.Contact.Value,
			Label:      route.Contact.Label,
			VerifiedAt: route.Contact.VerifiedAt,
		}
	}

	return &RouteResponse{
		UID:       route.UID,
		Enabled:   route.Enabled,
		Position:  route.Position,
		Contact:   contactResp,
		CreatedAt: route.CreatedAt,
	}
}

func toRouteResponses(routes []*models.UserNotificationRoute) []*RouteResponse {
	out := make([]*RouteResponse, len(routes))
	for i, route := range routes {
		out[i] = toRouteResponse(route)
	}

	return out
}

// SendTestNotification dispatches a test notification for the given route.
func (s *Service) SendTestNotification(
	ctx context.Context, orgSlug string, user *models.User, routeUID string,
	emailSender EmailSender, slackClient SlackDMSender, wpOpts webpush.Options,
) error {
	orgUID, err := s.resolveOrgUID(ctx, orgSlug)
	if err != nil {
		return err
	}

	routes, err := s.db.ListUserContactsWithRoutes(ctx, user.UID, orgUID)
	if err != nil {
		return fmt.Errorf("list routes: %w", err)
	}

	var target *models.UserNotificationRoute

	for _, ro := range routes {
		if ro.UID == routeUID {
			target = ro

			break
		}
	}

	if target == nil || target.Contact == nil {
		return ErrRouteNotFound
	}

	return s.dispatchTestRoute(ctx, orgUID, target, emailSender, slackClient, wpOpts)
}

// dispatchTestRoute delivers a test notification for a single route.
func (s *Service) dispatchTestRoute(
	ctx context.Context, orgUID string,
	target *models.UserNotificationRoute,
	emailSender EmailSender, slackClient SlackDMSender, wpOpts webpush.Options,
) error {
	switch target.Contact.Type {
	case models.UserContactTypeEmail:
		if emailSender == nil {
			return ErrEmailSenderNotConfigured
		}

		return emailSender.SendTestEmail(ctx, target.Contact.Value)
	case models.UserContactTypeSlackUser:
		return s.dispatchTestSlack(ctx, orgUID, target.Contact.Value, slackClient)
	case models.UserContactTypeTelegram:
		return s.dispatchTestTelegram(ctx, target.Contact.Value)
	case models.UserContactTypeWebPush:
		if wpOpts.VAPIDPublicKey == "" {
			return ErrWebPushNotConfigured
		}

		return webpush.Send(ctx, wpOpts, target.Contact.Value, webpush.Message{
			Title: "Test alert from SolidPing",
			Body:  "This is a test notification.",
			URL:   "",
		})
	default:
		return fmt.Errorf("provider not configured for contact type %q", target.Contact.Type) //nolint:err113
	}
}

// dispatchTestTelegram sends a test message to an already-connected chat.
//
// Configured(), not Active(): messaging a chat that is already linked needs
// only the bot token. The @username is required solely to BUILD a connect link,
// and gating the test on it would fail the one button a user presses to check
// the setup they have just completed.
func (s *Service) dispatchTestTelegram(ctx context.Context, chatID string) error {
	client, err := telegram.NewClientFromConfig(&s.telegramCfg)
	if err != nil {
		return ErrTelegramNotEnabled
	}

	if _, err := client.SendMessage(ctx, &telegram.Message{
		ChatID: chatID,
		HTML:   telegram.BuildTestHTML(),
	}); err != nil {
		return fmt.Errorf("send telegram test message: %w", err)
	}

	return nil
}

// dispatchTestSlack sends a test Slack DM for the given user ID.
func (s *Service) dispatchTestSlack(
	ctx context.Context, orgUID, slackUserID string, slackClient SlackDMSender,
) error {
	slackChannel, chErr := s.db.GetSlackChannelForOrg(ctx, orgUID)
	if chErr != nil {
		if errors.Is(chErr, sql.ErrNoRows) {
			return ErrNoSlackChannelForOrg
		}

		return fmt.Errorf("load slack channel: %w", chErr)
	}

	if slackClient == nil {
		return ErrSlackClientNotConfigured
	}

	return slackClient.SendDMTest(ctx, slackChannel, slackUserID)
}
