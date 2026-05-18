// Package usernotifications provides HTTP handlers and business logic for
// per-user notification contact management and route configuration.
package usernotifications

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Service errors.
var (
	ErrOrgNotFound     = errors.New("organization not found")
	ErrRouteNotFound   = errors.New("notification route not found")
	ErrContactNotFound = errors.New("notification contact not found")
	ErrUserNotFound    = errors.New("user not found")
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
}

// NewService builds a service.
func NewService(dbService db.Service) *Service {
	return &Service{db: dbService}
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
	ch, err := s.db.GetSlackChannelForOrg(ctx, orgUID)
	if err != nil {
		return nil
	}

	// Is there already a slack_user contact for this provider ID?
	for _, r := range existing {
		if r.Contact != nil &&
			r.Contact.Type == models.UserContactTypeSlackUser &&
			r.Contact.Value == slackProvider.ProviderID {
			return nil // already added
		}
	}

	settings, err := models.SlackSettingsFromJSONMap(ch.Settings)
	if err != nil {
		return nil
	}

	return &SlackSuggestion{
		SlackUserID:   slackProvider.ProviderID,
		WorkspaceName: settings.TeamName,
		ChannelUID:    ch.UID,
	}
}

// findRouteByContact returns the route matching contactType/contactValue or nil.
func findRouteByContact(routes []*models.UserNotificationRoute, contactType, contactValue string) *RouteResponse {
	for _, r := range routes {
		if r.Contact != nil && r.Contact.Type == contactType && r.Contact.Value == contactValue {
			return toRouteResponse(r)
		}
	}

	return nil
}

// CreateContact creates a new contact + route.
func (s *Service) CreateContact(
	ctx context.Context, orgSlug string, user *models.User, req CreateContactRequest,
) (*RouteResponse, error) {
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
	contact := models.NewUserContact(user.UID, orgUID, req.Type, req.Value, req.Label)

	if req.Type == models.UserContactTypeEmail {
		now := time.Now()
		contact.VerifiedAt = &now
	}

	if err := s.db.UpsertUserContact(ctx, contact); err != nil {
		return nil, fmt.Errorf("create contact: %w", err)
	}

	return s.ensureRouteAfterUpsert(ctx, user, orgUID, contact, req.Type, req.Value, position)
}

// ensureRouteAfterUpsert reloads routes after an upsert and returns the matching route,
// creating the route row if it doesn't exist yet.
func (s *Service) ensureRouteAfterUpsert(
	ctx context.Context, user *models.User, orgUID string,
	contact *models.UserContact, contactType, contactValue string, position int,
) (*RouteResponse, error) {
	routes, err := s.db.ListUserContactsWithRoutes(ctx, user.UID, orgUID)
	if err != nil {
		return nil, fmt.Errorf("reload routes: %w", err)
	}

	if resp := findRouteByContact(routes, contactType, contactValue); resp != nil {
		return resp, nil
	}

	// No route yet — create it.
	route := models.NewUserNotificationRoute(user.UID, orgUID, contact.UID, position)
	if _, insertErr := s.db.DB().NewInsert().Model(route).
		On("CONFLICT (contact_uid) DO NOTHING").
		Exec(ctx); insertErr != nil {
		return nil, fmt.Errorf("create route: %w", insertErr)
	}

	routes, err = s.db.ListUserContactsWithRoutes(ctx, user.UID, orgUID)
	if err != nil {
		return nil, fmt.Errorf("reload routes after create: %w", err)
	}

	if resp := findRouteByContact(routes, contactType, contactValue); resp != nil {
		return resp, nil
	}

	return nil, errors.New("route not found after creation")
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
		if err := s.db.SetRouteEnabled(ctx, routeUID, *req.Enabled); err != nil {
			return nil, fmt.Errorf("set route enabled: %w", err)
		}
	}

	if len(req.RouteUIDs) > 0 {
		if err := s.db.ReorderRoutes(ctx, user.UID, orgUID, req.RouteUIDs); err != nil {
			return nil, fmt.Errorf("reorder routes: %w", err)
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
func toRouteResponse(r *models.UserNotificationRoute) *RouteResponse {
	cr := ContactResponse{}
	if r.Contact != nil {
		cr = ContactResponse{
			UID:        r.Contact.UID,
			Type:       r.Contact.Type,
			Value:      r.Contact.Value,
			Label:      r.Contact.Label,
			VerifiedAt: r.Contact.VerifiedAt,
		}
	}

	return &RouteResponse{
		UID:       r.UID,
		Enabled:   r.Enabled,
		Position:  r.Position,
		Contact:   cr,
		CreatedAt: r.CreatedAt,
	}
}

func toRouteResponses(routes []*models.UserNotificationRoute) []*RouteResponse {
	out := make([]*RouteResponse, len(routes))
	for i, r := range routes {
		out[i] = toRouteResponse(r)
	}

	return out
}

// SendTestNotification dispatches a test notification for the given route.
func (s *Service) SendTestNotification(
	ctx context.Context, orgSlug string, user *models.User, routeUID string,
	emailSender EmailSender, slackClient SlackDMSender,
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

	for _, r := range routes {
		if r.UID == routeUID {
			target = r

			break
		}
	}

	if target == nil || target.Contact == nil {
		return ErrRouteNotFound
	}

	switch target.Contact.Type {
	case models.UserContactTypeEmail:
		if emailSender == nil {
			return errors.New("email sender not configured")
		}

		return emailSender.SendTestEmail(ctx, target.Contact.Value)
	case models.UserContactTypeSlackUser:
		ch, chErr := s.db.GetSlackChannelForOrg(ctx, orgUID)
		if chErr != nil {
			if errors.Is(chErr, sql.ErrNoRows) {
				return errors.New("no Slack channel configured for this organization")
			}

			return fmt.Errorf("load slack channel: %w", chErr)
		}

		if slackClient == nil {
			return errors.New("slack client not configured")
		}

		return slackClient.SendDMTest(ctx, ch, target.Contact.Value)
	default:
		return fmt.Errorf("provider not configured for contact type %q", target.Contact.Type)
	}
}
