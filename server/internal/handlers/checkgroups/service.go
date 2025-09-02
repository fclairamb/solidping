// Package checkgroups provides HTTP handlers for check group management endpoints.
package checkgroups

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// slugRegex validates slug format: lowercase letter, then 2-39 lowercase letters/digits/hyphens.
var slugRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{2,39}$`)

// slugInvalidCharsRegex matches characters that are not valid in a slug.
var slugInvalidCharsRegex = regexp.MustCompile(`[^a-z0-9-]`)

var (
	// ErrOrganizationNotFound is returned when an organization is not found.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrCheckGroupNotFound is returned when a check group is not found.
	ErrCheckGroupNotFound = errors.New("check group not found")
	// ErrSlugConflict is returned when a slug already exists.
	ErrSlugConflict = errors.New("slug already exists")
	// ErrInvalidSlugFormat is returned when a slug has an invalid format.
	ErrInvalidSlugFormat = errors.New("invalid slug format")
)

func validateSlug(slug string) error {
	if slug == "" {
		return nil
	}

	if _, err := uuid.Parse(slug); err == nil {
		return ErrInvalidSlugFormat
	}

	if !slugRegex.MatchString(slug) {
		return ErrInvalidSlugFormat
	}

	return nil
}

func sanitizeSlug(name string) string {
	const maxSlugLen = 40

	slug := strings.ToLower(name)
	slug = slugInvalidCharsRegex.ReplaceAllString(slug, "-")

	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}

	slug = strings.Trim(slug, "-")

	if len(slug) == 0 || (slug[0] >= '0' && slug[0] <= '9') {
		slug = "x" + slug
	}

	if len(slug) > maxSlugLen {
		slug = strings.TrimRight(slug[:maxSlugLen], "-")
	}

	return slug
}

// Service provides business logic for check group management.
type Service struct {
	db db.Service
}

// NewService creates a new check groups service.
func NewService(dbService db.Service) *Service {
	return &Service{db: dbService}
}

// CheckGroupResponse represents a check group in API responses.
type CheckGroupResponse struct {
	UID         string    `json:"uid"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description *string   `json:"description,omitempty"`
	SortOrder   int16     `json:"sortOrder"`
	CheckCount  int       `json:"checkCount"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// CreateCheckGroupRequest represents a request to create a new check group.
type CreateCheckGroupRequest struct {
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Description *string `json:"description"`
	SortOrder   *int16  `json:"sortOrder"`
}

// UpdateCheckGroupRequest represents a request to update a check group.
type UpdateCheckGroupRequest struct {
	Name        *string `json:"name,omitempty"`
	Slug        *string `json:"slug,omitempty"`
	Description *string `json:"description,omitempty"`
	SortOrder   *int16  `json:"sortOrder,omitempty"`
}

// ListCheckGroups retrieves all check groups for an organization.
func (s *Service) ListCheckGroups(ctx context.Context, orgSlug string) ([]CheckGroupResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	groups, err := s.db.ListCheckGroups(ctx, org.UID)
	if err != nil {
		return nil, err
	}

	responses := make([]CheckGroupResponse, len(groups))
	for i, g := range groups {
		responses[i] = convertGroupToResponse(g)
	}

	return responses, nil
}

// CreateCheckGroup creates a new check group.
//
//nolint:cyclop // Complex due to slug generation and validation
func (s *Service) CreateCheckGroup(
	ctx context.Context, orgSlug string, req CreateCheckGroupRequest,
) (CheckGroupResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return CheckGroupResponse{}, ErrOrganizationNotFound
	}

	if req.Name == "" {
		return CheckGroupResponse{}, ErrInvalidSlugFormat
	}

	// Handle slug
	userProvidedSlug := req.Slug != ""
	slug := req.Slug

	if userProvidedSlug {
		if errValidate := validateSlug(slug); errValidate != nil {
			return CheckGroupResponse{}, errValidate
		}
	} else {
		slug = sanitizeSlug(req.Name)
	}

	// Check for slug conflict
	existing, errGet := s.db.GetCheckGroupBySlug(ctx, org.UID, slug)
	if errGet != nil && !errors.Is(errGet, sql.ErrNoRows) {
		return CheckGroupResponse{}, errGet
	}

	if existing != nil {
		if userProvidedSlug {
			return CheckGroupResponse{}, ErrSlugConflict
		}
		// Auto-generated: append number
		baseSlug := slug
		for i := 2; i <= 99; i++ {
			candidate := baseSlug + "-" + strings.Repeat("", 0) + itoa(i)
			existing, errGet = s.db.GetCheckGroupBySlug(ctx, org.UID, candidate)
			if errGet != nil && !errors.Is(errGet, sql.ErrNoRows) {
				return CheckGroupResponse{}, errGet
			}
			if existing == nil {
				slug = candidate
				break
			}
		}
		if existing != nil {
			return CheckGroupResponse{}, ErrSlugConflict
		}
	}

	group := models.NewCheckGroup(org.UID, req.Name, slug)

	if req.Description != nil {
		group.Description = req.Description
	}

	if req.SortOrder != nil {
		group.SortOrder = *req.SortOrder
	}

	if err := s.db.CreateCheckGroup(ctx, group); err != nil {
		return CheckGroupResponse{}, err
	}

	return convertGroupToResponse(group), nil
}

// GetCheckGroup retrieves a single check group by UID or slug.
func (s *Service) GetCheckGroup(
	ctx context.Context, orgSlug, identifier string,
) (CheckGroupResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return CheckGroupResponse{}, ErrOrganizationNotFound
	}

	group, err := s.db.GetCheckGroupByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || group == nil {
		return CheckGroupResponse{}, ErrCheckGroupNotFound
	}

	return convertGroupToResponse(group), nil
}

// UpdateCheckGroup updates an existing check group.
func (s *Service) UpdateCheckGroup(
	ctx context.Context, orgSlug, identifier string, req UpdateCheckGroupRequest,
) (CheckGroupResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return CheckGroupResponse{}, ErrOrganizationNotFound
	}

	group, err := s.db.GetCheckGroupByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || group == nil {
		return CheckGroupResponse{}, ErrCheckGroupNotFound
	}

	// Validate slug if provided
	if req.Slug != nil && *req.Slug != "" { //nolint:nestif // Slug validation requires nested checks
		if errValidate := validateSlug(*req.Slug); errValidate != nil {
			return CheckGroupResponse{}, errValidate
		}

		// Check for slug conflict (only if different from current)
		if *req.Slug != group.Slug {
			existing, errGet := s.db.GetCheckGroupBySlug(ctx, org.UID, *req.Slug)
			if errGet != nil && !errors.Is(errGet, sql.ErrNoRows) {
				return CheckGroupResponse{}, errGet
			}
			if existing != nil {
				return CheckGroupResponse{}, ErrSlugConflict
			}
		}
	}

	update := models.CheckGroupUpdate{
		Name:        req.Name,
		Slug:        req.Slug,
		Description: req.Description,
		SortOrder:   req.SortOrder,
	}

	if errUpdate := s.db.UpdateCheckGroup(ctx, org.UID, group.UID, &update); errUpdate != nil {
		return CheckGroupResponse{}, errUpdate
	}

	// Fetch updated group
	updatedGroup, err := s.db.GetCheckGroup(ctx, org.UID, group.UID)
	if err != nil {
		return CheckGroupResponse{}, err
	}

	return convertGroupToResponse(updatedGroup), nil
}

// DeleteCheckGroup deletes a check group by UID or slug (soft delete).
func (s *Service) DeleteCheckGroup(ctx context.Context, orgSlug, identifier string) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return ErrOrganizationNotFound
	}

	group, err := s.db.GetCheckGroupByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || group == nil {
		return ErrCheckGroupNotFound
	}

	return s.db.DeleteCheckGroup(ctx, group.UID)
}

func convertGroupToResponse(group *models.CheckGroup) CheckGroupResponse {
	return CheckGroupResponse{
		UID:         group.UID,
		Name:        group.Name,
		Slug:        group.Slug,
		Description: group.Description,
		SortOrder:   group.SortOrder,
		CheckCount:  group.CheckCount,
		CreatedAt:   group.CreatedAt,
		UpdatedAt:   group.UpdatedAt,
	}
}

// itoa converts an int to string (simple helper to avoid importing strconv).
func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}

	return itoa(n/10) + string(rune('0'+n%10))
}
