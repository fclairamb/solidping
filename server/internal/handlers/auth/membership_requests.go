package auth

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

const (
	defaultMembershipRequestCooldownDays = 7
)

// MembershipRequestCreateRequest is the body of POST
// /api/v1/auth/membership-requests.
type MembershipRequestCreateRequest struct {
	OrgSlug string `json:"orgSlug"`
	Message string `json:"message,omitempty"`
}

// MembershipRequestApproveRequest is the body of approve.
type MembershipRequestApproveRequest struct {
	Role string `json:"role,omitempty"`
}

// MembershipRequestRejectRequest is the body of reject.
type MembershipRequestRejectRequest struct {
	Reason string `json:"reason,omitempty"`
}

// MembershipRequestAdminView is what admins see — includes the requesting
// user's identity. Used in GET /api/v1/orgs/{org}/membership-requests.
type MembershipRequestAdminView struct {
	UID            string                         `json:"uid"`
	User           UserInfo                       `json:"user"`
	Status         models.MembershipRequestStatus `json:"status"`
	Message        string                         `json:"message,omitempty"`
	DecisionReason string                         `json:"decisionReason,omitempty"`
	CreatedAt      time.Time                      `json:"createdAt"`
	DecidedAt      *time.Time                     `json:"decidedAt,omitempty"`
}

// MembershipRequestListResponse wraps the data array per API conventions.
type MembershipRequestListResponse struct {
	Data []MembershipRequestSummary `json:"data"`
}

// MembershipRequestAdminListResponse wraps admin-side list.
type MembershipRequestAdminListResponse struct {
	Data []MembershipRequestAdminView `json:"data"`
}

// CreateMembershipRequest opens (or re-opens) a pending request.
func (s *Service) CreateMembershipRequest(
	ctx context.Context, userUID string, req MembershipRequestCreateRequest,
) (*MembershipRequestSummary, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, req.OrgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	if _, memberErr := s.db.GetMemberByUserAndOrg(ctx, userUID, org.UID); memberErr == nil {
		return nil, ErrAlreadyAMember
	}

	existing, lookupErr := s.db.GetMembershipRequestByOrgAndUser(ctx, org.UID, userUID)
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		return nil, lookupErr
	}

	var message *string
	if req.Message != "" {
		m := req.Message
		message = &m
	}

	if existing == nil {
		request := models.NewMembershipRequest(org.UID, userUID, message)
		if err := s.db.CreateMembershipRequest(ctx, request); err != nil {
			return nil, err
		}

		summary := buildMembershipSummary(request, org)

		return &summary, nil
	}

	switch existing.Status {
	case models.MembershipRequestStatusPending:
		return nil, ErrRequestPending
	case models.MembershipRequestStatusApproved:
		return nil, ErrAlreadyAMember
	case models.MembershipRequestStatusRejected:
		if s.inCooldown(existing.DecidedAt) {
			return nil, ErrRequestCooldownActive
		}
	case models.MembershipRequestStatusCancelled:
		// no cooldown — fall through and re-open
	}

	existing.Status = models.MembershipRequestStatusPending
	existing.Message = message
	existing.DecisionReason = nil
	existing.DecidedAt = nil
	existing.DecidedByUID = nil

	if err := s.db.UpdateMembershipRequest(ctx, existing); err != nil {
		return nil, err
	}

	summary := buildMembershipSummary(existing, org)

	return &summary, nil
}

// CancelMembershipRequest lets the requester drop their own request.
// Returns ErrRequestNotFound on missing or non-owned rows.
func (s *Service) CancelMembershipRequest(
	ctx context.Context, userUID, requestUID string,
) error {
	request, err := s.db.GetMembershipRequest(ctx, requestUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRequestNotFound
		}

		return err
	}

	if request.UserUID != userUID {
		return ErrRequestNotFound
	}

	request.Status = models.MembershipRequestStatusCancelled
	now := time.Now()
	request.DecidedAt = &now

	return s.db.UpdateMembershipRequest(ctx, request)
}

// ListOwnMembershipRequests returns the user's own request history with
// org summaries.
func (s *Service) ListOwnMembershipRequests(
	ctx context.Context, userUID string,
) (*MembershipRequestListResponse, error) {
	requests, err := s.db.ListMembershipRequests(ctx, models.ListMembershipRequestsFilter{
		UserUID: userUID,
	})
	if err != nil {
		return nil, err
	}

	out := make([]MembershipRequestSummary, 0, len(requests))
	for _, request := range requests {
		out = append(out, buildMembershipSummary(request, request.Organization))
	}

	return &MembershipRequestListResponse{Data: out}, nil
}

// ListOrgMembershipRequests returns admin-facing rows for an org. The
// caller is expected to have validated admin role at the handler level.
func (s *Service) ListOrgMembershipRequests(
	ctx context.Context, orgSlug string, status models.MembershipRequestStatus,
) (*MembershipRequestAdminListResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	requests, err := s.db.ListMembershipRequests(ctx, models.ListMembershipRequestsFilter{
		OrganizationUID: org.UID,
		Status:          status,
	})
	if err != nil {
		return nil, err
	}

	out := make([]MembershipRequestAdminView, 0, len(requests))
	for _, request := range requests {
		view := MembershipRequestAdminView{
			UID:       request.UID,
			Status:    request.Status,
			CreatedAt: request.CreatedAt,
			DecidedAt: request.DecidedAt,
		}
		if request.User != nil {
			view.User = UserInfo{
				UID:       request.User.UID,
				Email:     request.User.Email,
				Name:      request.User.Name,
				AvatarURL: request.User.AvatarURL,
			}
		}
		if request.Message != nil {
			view.Message = *request.Message
		}
		if request.DecisionReason != nil {
			view.DecisionReason = *request.DecisionReason
		}
		out = append(out, view)
	}

	return &MembershipRequestAdminListResponse{Data: out}, nil
}

// ApproveMembershipRequest commits status + new membership in one tx.
func (s *Service) ApproveMembershipRequest(
	ctx context.Context, decidedByUID, orgSlug, requestUID string,
	roleStr string,
) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return ErrOrganizationNotFound
	}

	request, err := s.db.GetMembershipRequest(ctx, requestUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRequestNotFound
		}

		return err
	}

	if request.OrganizationUID != org.UID {
		return ErrRequestNotFound
	}

	role := models.MemberRoleUser
	switch roleStr {
	case string(models.MemberRoleAdmin):
		role = models.MemberRoleAdmin
	case string(models.MemberRoleViewer):
		role = models.MemberRoleViewer
	}

	now := time.Now()
	request.Status = models.MembershipRequestStatusApproved
	request.DecidedAt = &now
	request.DecidedByUID = &decidedByUID
	request.DecisionReason = nil

	member := models.NewOrganizationMember(org.UID, request.UserUID, role)

	if err := s.db.ApproveMembershipRequest(ctx, request, member); err != nil {
		return err
	}

	slog.InfoContext(ctx, "membership request approved",
		"requestUID", request.UID, "orgUID", org.UID, "userUID", request.UserUID)

	return nil
}

// RejectMembershipRequest moves a request to rejected with an optional
// reason.
func (s *Service) RejectMembershipRequest(
	ctx context.Context, decidedByUID, orgSlug, requestUID, reason string,
) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return ErrOrganizationNotFound
	}

	request, err := s.db.GetMembershipRequest(ctx, requestUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRequestNotFound
		}

		return err
	}

	if request.OrganizationUID != org.UID {
		return ErrRequestNotFound
	}

	now := time.Now()
	request.Status = models.MembershipRequestStatusRejected
	request.DecidedAt = &now
	request.DecidedByUID = &decidedByUID
	if reason != "" {
		r := reason
		request.DecisionReason = &r
	}

	return s.db.UpdateMembershipRequest(ctx, request)
}

// listPendingMembershipRequests is used by GetUserInfo to surface the
// user's own pending+rejected requests (the no-org screen reads both).
func (s *Service) listPendingMembershipRequests(
	ctx context.Context, userUID string,
) ([]MembershipRequestSummary, error) {
	requests, err := s.db.ListMembershipRequests(ctx, models.ListMembershipRequestsFilter{
		UserUID: userUID,
	})
	if err != nil {
		return nil, err
	}

	out := make([]MembershipRequestSummary, 0, len(requests))
	for _, request := range requests {
		switch request.Status {
		case models.MembershipRequestStatusPending,
			models.MembershipRequestStatusRejected:
			out = append(out, buildMembershipSummary(request, request.Organization))
		case models.MembershipRequestStatusApproved,
			models.MembershipRequestStatusCancelled:
			// not surfaced on /me
		}
	}

	return out, nil
}

// inCooldown reports whether a rejected request is still inside its
// cooldown window.
func (s *Service) inCooldown(decidedAt *time.Time) bool {
	if decidedAt == nil {
		return false
	}

	cooldown := membershipRequestCooldown(s.fullCfg)
	if cooldown <= 0 {
		return false
	}

	return time.Since(*decidedAt) < cooldown
}

// membershipRequestCooldown reads the cooldown duration. Centralized so we
// can swap the source (env, parameters) without touching call sites.
func membershipRequestCooldown(_ interface{}) time.Duration {
	return time.Duration(defaultMembershipRequestCooldownDays) * 24 * time.Hour
}

func buildMembershipSummary(
	request *models.MembershipRequest, org *models.Organization,
) MembershipRequestSummary {
	summary := MembershipRequestSummary{
		UID:       request.UID,
		Status:    request.Status,
		CreatedAt: request.CreatedAt,
		DecidedAt: request.DecidedAt,
	}

	if org != nil {
		summary.Organization = OrganizationRef{
			UID:  org.UID,
			Slug: org.Slug,
			Name: org.Name,
		}
	}

	if request.Message != nil {
		summary.Message = *request.Message
	}
	if request.DecisionReason != nil {
		summary.DecisionReason = *request.DecisionReason
	}

	return summary
}
