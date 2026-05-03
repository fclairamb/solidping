// Package labels powers the label autocomplete API. It exposes distinct keys
// (or distinct values for a given key) used by checks in an organization,
// together with usage counts so the dashboard can sort suggestions by
// popularity.
package labels

import (
	"context"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ErrOrganizationNotFound is returned when the org slug doesn't resolve.
var ErrOrganizationNotFound = errors.New("organization not found")

// SuggestionResponse is the JSON-shaped row returned to the API.
type SuggestionResponse struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// Service wraps DB calls for label suggestions.
type Service struct {
	db db.Service
}

// NewService builds a Service.
func NewService(dbService db.Service) *Service {
	return &Service{db: dbService}
}

// ListLabels returns either distinct keys (when key == "") or distinct values
// for that key. The caller is expected to have already passed limit through
// any clamping logic; we trust it here.
func (s *Service) ListLabels(
	ctx context.Context, orgSlug, key, query string, limit int,
) ([]SuggestionResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil || org == nil {
		return nil, ErrOrganizationNotFound
	}

	var (
		raw     []models.LabelSuggestion
		listErr error
	)

	if key == "" {
		raw, listErr = s.db.ListDistinctLabelKeys(ctx, org.UID, query, limit)
	} else {
		raw, listErr = s.db.ListDistinctLabelValues(ctx, org.UID, key, query, limit)
	}

	if listErr != nil {
		return nil, fmt.Errorf("list label suggestions: %w", listErr)
	}

	out := make([]SuggestionResponse, len(raw))
	for i, r := range raw {
		out[i] = SuggestionResponse{Value: r.Value, Count: r.Count}
	}

	return out, nil
}
