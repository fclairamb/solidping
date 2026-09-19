package system

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// The user directory's paging bounds. Mirrors the entitlements admin editor's
// bounds (server/internal/handlers/entitlements/admin.go) — same shape, same
// numbers, for the same reason (a page here can be larger than an org list).
const (
	defaultUsersPageSize = 50
	maxUsersPageSize     = 200
)

// AdminOrgMembership is one organization a directory user belongs to.
type AdminOrgMembership struct {
	UID      string     `json:"uid"`
	Slug     string     `json:"slug"`
	Name     string     `json:"name"`
	Role     string     `json:"role"`
	JoinedAt *time.Time `json:"joinedAt,omitempty"`
}

// AdminUserRow is one line of the superadmin user directory. It is a deliberate
// allow-list projection of models.User — never the struct itself, which also
// carries PasswordHash, TOTPSecret and TOTPRecoveryCodes.
type AdminUserRow struct {
	UID                string               `json:"uid"`
	Email              string               `json:"email"`
	Name               string               `json:"name"`
	AvatarURL          string               `json:"avatarUrl"`
	SuperAdmin         bool                 `json:"superAdmin"`
	Demo               bool                 `json:"demo"`
	EmailVerified      bool                 `json:"emailVerified"`
	TOTPEnabled        bool                 `json:"totpEnabled"`
	MustChangePassword bool                 `json:"mustChangePassword"`
	HasPassword        bool                 `json:"hasPassword"`
	LastActiveAt       *time.Time           `json:"lastActiveAt,omitempty"`
	CreatedAt          time.Time            `json:"createdAt"`
	Orgs               []AdminOrgMembership `json:"orgs"`
}

// AdminUsersListResponse wraps the page, per the repo's list convention.
type AdminUsersListResponse struct {
	Data []AdminUserRow `json:"data"`
	// Total is how many users matched the search, before paging.
	Total int `json:"total"`
}

// SearchUsers pages the global user directory: one query for the page of
// users, one batched query for their memberships (never 1+N).
func (s *Service) SearchUsers(
	ctx context.Context, filter models.UserSearchFilter,
) (*AdminUsersListResponse, error) {
	users, total, err := s.db.SearchUsers(ctx, filter)
	if err != nil {
		return nil, err
	}

	uids := make([]string, len(users))
	for i, u := range users {
		uids[i] = u.UID
	}

	members, err := s.db.ListMembersByUsers(ctx, uids)
	if err != nil {
		return nil, err
	}

	membershipsByUser := make(map[string][]AdminOrgMembership, len(uids))

	for _, member := range members {
		if member.Organization == nil {
			continue
		}

		membershipsByUser[member.UserUID] = append(membershipsByUser[member.UserUID], AdminOrgMembership{
			UID:      member.Organization.UID,
			Slug:     member.Organization.Slug,
			Name:     member.Organization.Name,
			Role:     string(member.Role),
			JoinedAt: member.JoinedAt,
		})
	}

	rows := make([]AdminUserRow, len(users))

	for i, user := range users {
		orgs := membershipsByUser[user.UID]
		if orgs == nil {
			orgs = []AdminOrgMembership{}
		}

		rows[i] = AdminUserRow{
			UID:                user.UID,
			Email:              user.Email,
			Name:               user.Name,
			AvatarURL:          user.AvatarURL,
			SuperAdmin:         user.SuperAdmin,
			Demo:               user.Demo,
			EmailVerified:      user.EmailVerifiedAt != nil,
			TOTPEnabled:        user.TOTPEnabled,
			MustChangePassword: user.MustChangePassword,
			HasPassword:        user.PasswordHash != nil,
			LastActiveAt:       user.LastActiveAt,
			CreatedAt:          user.CreatedAt,
			Orgs:               orgs,
		}
	}

	return &AdminUsersListResponse{Data: rows, Total: total}, nil
}

// ListUsers handles GET /api/v1/system/users — superadmin only. See
// wiki/api-specification/system.md for the full contract.
func (h *Handler) ListUsers(writer http.ResponseWriter, req *http.Request) error {
	query := req.URL.Query()

	limit, err := base.ParsePageLimit(query, defaultUsersPageSize, maxUsersPageSize)
	if err != nil {
		limit = defaultUsersPageSize
	}

	offset := 0
	if raw := query.Get("offset"); raw != "" {
		if parsed, parseErr := strconv.Atoi(raw); parseErr == nil && parsed > 0 {
			offset = parsed
		}
	}

	resp, err := h.svc.SearchUsers(req.Context(), models.UserSearchFilter{
		Query:  query.Get("q"),
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}
