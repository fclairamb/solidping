// Package checktypes provides HTTP handlers for check type metadata endpoints.
package checktypes

import (
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// CheckTypeResponse is the JSON representation of a check type with its status.
type CheckTypeResponse struct {
	Type           string   `json:"type"`
	Description    string   `json:"description"`
	Labels         []string `json:"labels"`
	Enabled        bool     `json:"enabled"`
	DisabledReason string   `json:"disabledReason,omitempty"`
}

// ListCheckTypesResponse wraps the list of check types.
type ListCheckTypesResponse struct {
	Data []CheckTypeResponse `json:"data"`
}

// Service provides business logic for check type queries.
type Service struct {
	resolver *checkerdef.ActivationResolver
}

// NewService creates a new check types service.
func NewService(resolver *checkerdef.ActivationResolver) *Service {
	return &Service{resolver: resolver}
}

// ListServerCheckTypes returns all check types with server-level activation status.
func (s *Service) ListServerCheckTypes() ListCheckTypesResponse {
	statuses := s.resolver.ListAllWithStatus(nil)

	return toResponse(statuses)
}

// ListOrgCheckTypes returns all check types with org-level activation status.
func (s *Service) ListOrgCheckTypes(orgDisabled []string) ListCheckTypesResponse {
	statuses := s.resolver.ListAllWithStatus(orgDisabled)

	return toResponse(statuses)
}

func toResponse(statuses []checkerdef.CheckTypeStatus) ListCheckTypesResponse {
	data := make([]CheckTypeResponse, 0, len(statuses))

	for idx := range statuses {
		data = append(data, CheckTypeResponse{
			Type:           string(statuses[idx].Type),
			Description:    statuses[idx].Description,
			Labels:         statuses[idx].Labels,
			Enabled:        statuses[idx].Enabled,
			DisabledReason: statuses[idx].DisabledReason,
		})
	}

	return ListCheckTypesResponse{Data: data}
}
