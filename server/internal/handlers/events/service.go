// Package events provides event listing functionality.
package events

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Service errors.
var (
	ErrOrganizationNotFound = errors.New("organization not found")
)

// Service provides event listing functionality.
type Service struct {
	db db.Service
}

// NewService creates a new event service.
func NewService(dbService db.Service) *Service {
	return &Service{db: dbService}
}

// ListEventsOptions contains options for listing events.
type ListEventsOptions struct {
	IncidentUID *string
	CheckUID    *string
	EventTypes  []string
	Since       *time.Time
	Until       *time.Time
	Cursor      string
	Size        int
}

// EventResponse represents an event in API responses.
type EventResponse struct {
	UID         string         `json:"uid"`
	IncidentUID *string        `json:"incidentUid,omitempty"`
	CheckUID    *string        `json:"checkUid,omitempty"`
	EventType   string         `json:"eventType"`
	ActorType   string         `json:"actorType"`
	ActorUID    *string        `json:"actorUid,omitempty"`
	Payload     map[string]any `json:"payload,omitempty"`
	CreatedAt   time.Time      `json:"createdAt"`
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

// ListEvents lists events for an organization.
func (s *Service) ListEvents(
	ctx context.Context, orgSlug string, opts *ListEventsOptions,
) (*ListEventsResponse, error) {
	// Get organization
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	// Build filter
	filter := &models.ListEventsFilter{
		OrganizationUID: org.UID,
		IncidentUID:     opts.IncidentUID,
		CheckUID:        opts.CheckUID,
		Since:           opts.Since,
		Until:           opts.Until,
		Limit:           opts.Size + 1, // Fetch one extra to determine hasMore
	}

	// Convert event type strings to EventType values
	for _, typeStr := range opts.EventTypes {
		filter.EventTypes = append(filter.EventTypes, models.EventType(typeStr))
	}

	// TODO: Parse cursor

	events, err := s.db.ListEvents(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to list events: %w", err)
	}

	// Determine if there are more results
	hasMore := len(events) > opts.Size
	if hasMore {
		events = events[:opts.Size]
	}

	// Build response
	response := &ListEventsResponse{
		Data: make([]EventResponse, 0, len(events)),
		Pagination: PaginationResponse{
			Size: opts.Size,
		},
	}

	for _, evt := range events {
		response.Data = append(response.Data, EventResponse{
			UID:         evt.UID,
			IncidentUID: evt.IncidentUID,
			CheckUID:    evt.CheckUID,
			EventType:   string(evt.EventType),
			ActorType:   string(evt.ActorType),
			ActorUID:    evt.ActorUID,
			Payload:     evt.Payload,
			CreatedAt:   evt.CreatedAt,
		})
	}

	// Set cursor if there are more results
	if hasMore && len(events) > 0 {
		lastEvent := events[len(events)-1]
		response.Pagination.Cursor = lastEvent.UID
	}

	return response, nil
}
