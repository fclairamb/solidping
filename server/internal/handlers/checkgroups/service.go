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

// slugRegex validates slug format: lowercase letter, then 2-99 lowercase letters/digits/hyphens.
var slugRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{2,99}$`)

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
	UID         string  `json:"uid"`
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Description *string `json:"description,omitempty"`
	SortOrder   int16   `json:"sortOrder"`
	CheckCount  int     `json:"checkCount"`
	// Status is the derived, read-time rollup of enabled member checks'
	// statuses (spec 2026-08-01-01): see models.RollupGroupStatus for the
	// exact rules. Never stored — recomputed on every read.
	Status string `json:"status"`
	// MemberStatusCounts is the per-status count of enabled member checks
	// (wire status name -> count), omitting statuses with zero members, so
	// clients can render e.g. "3/4 up" without a second call.
	MemberStatusCounts map[string]int `json:"memberStatusCounts,omitempty"`
	// EscalationPolicyUID is the group-level escalation policy that its member
	// checks inherit when they have no policy of their own. nil = no group
	// policy (checks then fall back to the org default, then none).
	EscalationPolicyUID *string   `json:"escalationPolicyUid,omitempty"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

// CreateCheckGroupRequest represents a request to create a new check group.
type CreateCheckGroupRequest struct {
	Name                string  `json:"name"`
	Slug                string  `json:"slug"`
	Description         *string `json:"description"`
	SortOrder           *int16  `json:"sortOrder"`
	EscalationPolicyUID *string `json:"escalationPolicyUid"`
}

// UpdateCheckGroupRequest represents a request to update a check group.
type UpdateCheckGroupRequest struct {
	Name        *string `json:"name,omitempty"`
	Slug        *string `json:"slug,omitempty"`
	Description *string `json:"description,omitempty"`
	SortOrder   *int16  `json:"sortOrder,omitempty"`
	// EscalationPolicyUID: a non-empty UID sets the group policy; an empty
	// string clears it; omit to leave unchanged.
	EscalationPolicyUID *string `json:"escalationPolicyUid,omitempty"`
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

	statusCounts, err := s.db.GetCheckGroupStatusCounts(ctx, org.UID)
	if err != nil {
		return nil, err
	}

	responses := make([]CheckGroupResponse, len(groups))
	for i, g := range groups {
		responses[i] = convertGroupToResponse(g, statusCounts[g.UID])
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

	if req.EscalationPolicyUID != nil && *req.EscalationPolicyUID != "" {
		group.EscalationPolicyUID = req.EscalationPolicyUID
	}

	if err := s.db.CreateCheckGroup(ctx, group); err != nil {
		return CheckGroupResponse{}, err
	}

	// A brand-new group has no member checks yet — no query needed, the
	// rollup of an empty/nil counts map is always "created".
	return convertGroupToResponse(group, nil), nil
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

	statusCounts, err := s.db.GetCheckGroupStatusCounts(ctx, org.UID)
	if err != nil {
		return CheckGroupResponse{}, err
	}

	return convertGroupToResponse(group, statusCounts[group.UID]), nil
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

	// Escalation policy: empty string clears, non-empty sets, omission leaves
	// untouched (mirrors the check form's inherit/set/silent semantics).
	if req.EscalationPolicyUID != nil {
		if *req.EscalationPolicyUID == "" {
			update.ClearEscalationPolicyUID = true
		} else {
			update.EscalationPolicyUID = req.EscalationPolicyUID
		}
	}

	if errUpdate := s.db.UpdateCheckGroup(ctx, org.UID, group.UID, &update); errUpdate != nil {
		return CheckGroupResponse{}, errUpdate
	}

	return s.fetchGroupResponse(ctx, org.UID, group.UID)
}

// fetchGroupResponse re-fetches a group by UID and its member status counts,
// combining them into the API response. Shared by GetCheckGroup and
// UpdateCheckGroup's post-write refetch.
func (s *Service) fetchGroupResponse(ctx context.Context, orgUID, groupUID string) (CheckGroupResponse, error) {
	group, err := s.db.GetCheckGroup(ctx, orgUID, groupUID)
	if err != nil {
		return CheckGroupResponse{}, err
	}

	statusCounts, err := s.db.GetCheckGroupStatusCounts(ctx, orgUID)
	if err != nil {
		return CheckGroupResponse{}, err
	}

	return convertGroupToResponse(group, statusCounts[group.UID]), nil
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

// convertGroupToResponse builds the API response for a group given the
// per-status counts of its enabled member checks (nil/empty for a group with
// no such members, e.g. one that was just created).
func convertGroupToResponse(group *models.CheckGroup, statusCounts map[models.CheckStatus]int) CheckGroupResponse {
	rollup := models.RollupGroupStatus(statusCounts)

	var memberStatusCounts map[string]int
	if len(statusCounts) > 0 {
		memberStatusCounts = make(map[string]int, len(statusCounts))

		for status, count := range statusCounts {
			if count == 0 {
				continue
			}

			memberStatusCounts[status.String()] = count
		}
	}

	return CheckGroupResponse{
		UID:                 group.UID,
		Name:                group.Name,
		Slug:                group.Slug,
		Description:         group.Description,
		SortOrder:           group.SortOrder,
		CheckCount:          group.CheckCount,
		Status:              rollup.String(),
		MemberStatusCounts:  memberStatusCounts,
		EscalationPolicyUID: group.EscalationPolicyUID,
		CreatedAt:           group.CreatedAt,
		UpdatedAt:           group.UpdatedAt,
	}
}

// itoa converts an int to string (simple helper to avoid importing strconv).
func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}

	return itoa(n/10) + string(rune('0'+n%10))
}
