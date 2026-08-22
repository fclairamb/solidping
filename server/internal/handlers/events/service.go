// Package events provides event listing functionality.
package events

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Service errors.
var (
	ErrOrganizationNotFound = errors.New("organization not found")
)

// RestrictedEventTypePrefixes are the event families only an org admin/owner
// (or a super admin) may read (spec 2026-08-21-09).
//
// The auth family records who signed in, from which address, and every failed
// attempt against every account in the org. That is a map of the org's people
// and their habits, and it is exactly what an attacker who compromised one
// low-privilege account would want next — so it is gated on the same role that
// can already see the member list and the session policy.
//
// This is enforced as a filter EXCLUSION rather than by rejecting `?type=auth`,
// which is what makes it hold for an unfiltered listing too.
//
//nolint:gochecknoglobals // static policy list, treated as a constant.
var RestrictedEventTypePrefixes = []string{"auth"}

// Service provides event listing functionality.
type Service struct {
	db db.Service
}

// NewService creates a new event service.
func NewService(dbService db.Service) *Service {
	return &Service{db: dbService}
}

// Caller identifies the authenticated principal reading the trail. The role is
// resolved LIVE from the membership row rather than trusted from the JWT: an
// access token minted while the user was an admin stays valid for its full
// lifetime, and "was an admin ten minutes ago" must not be enough to read the
// auth trail after a demotion.
type Caller struct {
	UserUID    string
	SuperAdmin bool
}

// ListEventsOptions contains options for listing events.
type ListEventsOptions struct {
	IncidentUID *string
	CheckUID    *string
	EventTypes  []string
	// EventTypePrefixes filters by family ("auth", "member", …) — the `type`
	// query parameter.
	EventTypePrefixes []string
	// ActorUID filters to one user's actions — the `actorUserUid` parameter.
	ActorUID *string
	// TargetUID / TargetType filter to the events about one object, or one
	// kind of object — the `targetUid` / `targetType` parameters.
	TargetUID  *string
	TargetType *string
	// SourceIP filters to one client address — the `sourceIp` parameter.
	// ADMIN-ONLY: see the note in ListEvents.
	SourceIP *string
	Since    *time.Time
	Until    *time.Time
	Cursor   string
	Size     int
	Caller   Caller
}

// EventResponse represents an event in API responses.
type EventResponse struct {
	UID         string  `json:"uid"`
	IncidentUID *string `json:"incidentUid,omitempty"`
	CheckUID    *string `json:"checkUid,omitempty"`
	EventType   string  `json:"eventType"`
	ActorType   string  `json:"actorType"`
	ActorUID    *string `json:"actorUid,omitempty"`
	// ActorName / ActorEmail are resolved from the users table for the page
	// being returned, so the UI does not have to fan out one request per row.
	// Absent for system events and for users that have since been deleted.
	ActorName  *string `json:"actorName,omitempty"`
	ActorEmail *string `json:"actorEmail,omitempty"`
	// SourceIP / UserAgent are admin-only, for the same reason the auth family
	// is: they describe where the org's people work from.
	SourceIP  *string        `json:"sourceIp,omitempty"`
	UserAgent *string        `json:"userAgent,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
}

// ListEventsResponse represents the response for listing events.
type ListEventsResponse struct {
	Data       []EventResponse    `json:"data"`
	Pagination PaginationResponse `json:"pagination"`
}

// PaginationResponse represents pagination info.
type PaginationResponse struct {
	Cursor string `json:"cursor,omitempty"`
	Size   int    `json:"size"`
}

// IsOrgAdmin reports whether the caller may read the restricted families.
func (s *Service) IsOrgAdmin(ctx context.Context, orgUID string, caller Caller) bool {
	if caller.SuperAdmin {
		return true
	}

	if caller.UserUID == "" {
		return false
	}

	member, err := s.db.GetMemberByUserAndOrg(ctx, caller.UserUID, orgUID)
	if err != nil || member == nil {
		return false
	}

	return member.Role.AtLeast(models.MemberRoleAdmin)
}

// ListEvents lists events for an organization.
func (s *Service) ListEvents(
	ctx context.Context, orgSlug string, opts *ListEventsOptions,
) (*ListEventsResponse, error) {
	// Get organization
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	isAdmin := s.IsOrgAdmin(ctx, org.UID, opts.Caller)

	// Build filter
	filter := &models.ListEventsFilter{
		OrganizationUID:   org.UID,
		IncidentUID:       opts.IncidentUID,
		CheckUID:          opts.CheckUID,
		EventTypePrefixes: opts.EventTypePrefixes,
		ActorUID:          opts.ActorUID,
		TargetUID:         opts.TargetUID,
		TargetType:        opts.TargetType,
		Since:             opts.Since,
		Until:             opts.Until,
		Limit:             opts.Size + 1, // Fetch one extra to determine hasMore
	}

	if !isAdmin {
		filter.ExcludeEventTypePrefixes = RestrictedEventTypePrefixes
	}

	// The source-IP filter is admin-only, not merely the source-IP COLUMN.
	// Withholding the value while honoring the predicate would turn the
	// filter into an oracle: a viewer asks for an address, gets a non-empty
	// page back, and has confirmed which addresses their colleagues work from
	// — the exact fact the column was withheld to protect. Silently ignoring
	// the parameter (rather than erroring) keeps the two roles' responses
	// shaped identically, so the endpoint does not become an oracle for
	// "am I an admin?" either.
	if isAdmin {
		filter.SourceIP = opts.SourceIP
	}

	// Convert event type strings to EventType values
	for _, typeStr := range opts.EventTypes {
		filter.EventTypes = append(filter.EventTypes, models.EventType(typeStr))
	}

	if ts, uid, ok := DecodeCursor(opts.Cursor); ok {
		filter.CursorTimestamp = &ts
		filter.CursorUID = &uid
	}

	events, err := s.db.ListEvents(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to list events: %w", err)
	}

	// Determine if there are more results
	hasMore := len(events) > opts.Size
	if hasMore {
		events = events[:opts.Size]
	}

	response := &ListEventsResponse{
		Data: make([]EventResponse, 0, len(events)),
		Pagination: PaginationResponse{
			Size: opts.Size,
		},
	}

	actors := s.resolveActors(ctx, events)

	for _, evt := range events {
		response.Data = append(response.Data, toResponse(evt, actors, isAdmin))
	}

	// Set cursor if there are more results
	if hasMore && len(events) > 0 {
		lastEvent := events[len(events)-1]
		response.Pagination.Cursor = EncodeCursor(lastEvent.CreatedAt, lastEvent.UID)
	}

	return response, nil
}

// toResponse renders one event, withholding the admin-only provenance fields
// from a caller that is not an org admin.
func toResponse(evt *models.Event, actors map[string]actorIdentity, isAdmin bool) EventResponse {
	item := EventResponse{
		UID:         evt.UID,
		IncidentUID: evt.IncidentUID,
		CheckUID:    evt.CheckUID,
		EventType:   string(evt.EventType),
		ActorType:   string(evt.ActorType),
		ActorUID:    evt.ActorUID,
		Payload:     evt.Payload,
		CreatedAt:   evt.CreatedAt,
	}

	if isAdmin {
		item.SourceIP = evt.SourceIP
		item.UserAgent = evt.UserAgent
	}

	if evt.ActorUID != nil {
		if actor, ok := actors[*evt.ActorUID]; ok {
			item.ActorName = actor.name
			item.ActorEmail = actor.email
		}
	}

	return item
}

type actorIdentity struct {
	name  *string
	email *string
}

// resolveActors loads the display identity for every distinct actor on the
// page. One lookup per DISTINCT actor, bounded by the page size (max 100) and
// in practice far below it — an audit page is usually a handful of people.
func (s *Service) resolveActors(ctx context.Context, events []*models.Event) map[string]actorIdentity {
	actors := make(map[string]actorIdentity)

	for _, evt := range events {
		if evt.ActorUID == nil || *evt.ActorUID == "" {
			continue
		}

		if _, seen := actors[*evt.ActorUID]; seen {
			continue
		}

		user, err := s.db.GetUser(ctx, *evt.ActorUID)
		if err != nil || user == nil {
			// A deleted user leaves an unresolvable actor_uid. The row still
			// belongs in the trail — an audit log that erases entries when the
			// person leaves is worthless — so it is recorded as seen with no
			// identity rather than skipped.
			actors[*evt.ActorUID] = actorIdentity{}

			continue
		}

		identity := actorIdentity{}
		if user.Name != "" {
			name := user.Name
			identity.name = &name
		}

		if user.Email != "" {
			email := user.Email
			identity.email = &email
		}

		actors[*evt.ActorUID] = identity
	}

	return actors
}

// cursorSeparator splits the two halves of an opaque cursor.
const cursorSeparator = "|"

// EncodeCursor builds the opaque keyset cursor for the last row on a page.
//
// It carries BOTH halves of the sort key (created_at, uid) because the ordering
// is not unique on created_at alone — several events can share a timestamp to
// the microsecond, and a timestamp-only cursor would either repeat or skip them
// at a page boundary. Before this the endpoint returned a bare UID and the
// service ignored it entirely, so "next page" silently returned page 1 forever.
func EncodeCursor(createdAt time.Time, uid string) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + cursorSeparator + uid

	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses a cursor produced by EncodeCursor. Anything it cannot
// parse — including the legacy bare-UID cursors that used to be handed out —
// reports !ok, and the caller starts from the first page. A malformed cursor is
// a client bug, not a reason to 500.
func DecodeCursor(cursor string) (time.Time, string, bool) {
	if cursor == "" {
		return time.Time{}, "", false
	}

	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", false
	}

	parts := strings.SplitN(string(decoded), cursorSeparator, 2)
	if len(parts) != 2 || parts[1] == "" {
		return time.Time{}, "", false
	}

	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", false
	}

	return ts, parts[1], true
}
