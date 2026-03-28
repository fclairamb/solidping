// Package checktypes provides HTTP handlers for check type metadata endpoints.
package checktypes

import (
	"sort"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
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

// SampleConfigResponse is the JSON representation of a single sample config.
type SampleConfigResponse struct {
	Name          string         `json:"name"`
	Slug          string         `json:"slug"`
	PeriodSeconds int            `json:"periodSeconds"`
	Config        map[string]any `json:"config"`
}

// CheckTypeSamplesResponse groups sample configs by check type.
type CheckTypeSamplesResponse struct {
	CheckType string                 `json:"checkType"`
	Samples   []SampleConfigResponse `json:"samples"`
}

// ListSamplesResponse wraps the list of check type samples.
type ListSamplesResponse struct {
	Data []CheckTypeSamplesResponse `json:"data"`
}

// Service provides business logic for check type queries.
type Service struct {
	resolver *checkerdef.ActivationResolver
	baseURL  string
}

// NewService creates a new check types service.
func NewService(resolver *checkerdef.ActivationResolver, baseURL string) *Service {
	return &Service{resolver: resolver, baseURL: baseURL}
}

// ListSampleConfigs returns sample configurations, optionally filtered by check type.
func (s *Service) ListSampleConfigs(filterType string) ListSamplesResponse {
	opts := &checkerdef.ListSampleOptions{
		Type:    checkerdef.Default,
		BaseURL: s.baseURL,
	}

	allSamples := registry.GetAllSampleConfigs(opts)

	data := make([]CheckTypeSamplesResponse, 0, len(allSamples))

	for checkType, specs := range allSamples {
		if filterType != "" && string(checkType) != filterType {
			continue
		}

		samples := make([]SampleConfigResponse, 0, len(specs))
		for idx := range specs {
			samples = append(samples, SampleConfigResponse{
				Name:          specs[idx].Name,
				Slug:          specs[idx].Slug,
				PeriodSeconds: int(specs[idx].Period / time.Second),
				Config:        specs[idx].Config,
			})
		}

		data = append(data, CheckTypeSamplesResponse{
			CheckType: string(checkType),
			Samples:   samples,
		})
	}

	sort.Slice(data, func(i, j int) bool {
		return data[i].CheckType < data[j].CheckType
	})

	return ListSamplesResponse{Data: data}
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
