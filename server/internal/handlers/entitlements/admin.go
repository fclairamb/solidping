package entitlements

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// The superadmin editor's paging bounds. Deliberately smaller than the audit
// page size: each row costs a resolve plus a checks-per-minute roll-up, so the
// page is what bounds the work, not the org count.
const (
	defaultAdminPageSize = 50
	maxAdminPageSize     = 200
)

// AdminOrgRow is one line of the superadmin org list: who the org is, what its
// limits resolve to, where those limits came from, and whether it is currently
// asking for more executions than its cap allows.
type AdminOrgRow struct {
	OrganizationUID string `json:"organizationUid"`
	Slug            string `json:"slug"`
	Name            string `json:"name"`

	entcore.Resolved

	// ChecksPerMinute is the demand-vs-cap pair (spec 2026-08-26-03). Absent
	// when it could not be computed — an over-limit badge is worth less than
	// the row it decorates, so its failure must not drop the row.
	ChecksPerMinute *entcore.ChecksPerMinute `json:"checksPerMinute,omitempty"`
	// OverCheckRate is the amber flag: scheduled demand exceeds the resolved
	// cap. Precomputed here so every client agrees on the comparison.
	OverCheckRate bool `json:"overCheckRate"`
	// AdminOverrideSince is when the current admin override was written. nil
	// unless the stored row is admin-sourced.
	AdminOverrideSince *time.Time `json:"adminOverrideSince,omitempty"`
}

// AdminListResponse wraps the page, per the repo's list convention.
type AdminListResponse struct {
	Data []AdminOrgRow `json:"data"`
	// Total is how many orgs matched the search, before paging.
	Total int `json:"total"`
}

// adminStoredRow is the projection of a stored org_entitlements row. The bun
// model has no JSON tags, and its raw shape is not an API contract — this is.
// It is nil in the response when the org has no stored row at all, which is
// exactly the "resolving to deployment defaults" state.
type adminStoredRow struct {
	Source       models.EntitlementSource `json:"source"`
	Limits       entcore.Limits           `json:"limits"`
	DisplayName  *string                  `json:"displayName,omitempty"`
	DisplayEmoji *string                  `json:"displayEmoji,omitempty"`
	ExternalRef  *string                  `json:"externalRef,omitempty"`
	ExpiresAt    *time.Time               `json:"expiresAt,omitempty"`
	LastSyncedAt *time.Time               `json:"lastSyncedAt,omitempty"`
	CreatedAt    time.Time                `json:"createdAt"`
	UpdatedAt    time.Time                `json:"updatedAt"`
}

// AdminOrgDetail is the editor's payload for one org.
type AdminOrgDetail struct {
	OrganizationUID string `json:"organizationUid"`
	Slug            string `json:"slug"`
	Name            string `json:"name"`

	entcore.Resolved

	ChecksPerMinute *entcore.ChecksPerMinute `json:"checksPerMinute,omitempty"`
	OverCheckRate   bool                     `json:"overCheckRate"`
	// Stored is the raw row, or nil when the org has never had one. The editor
	// needs it to distinguish "explicitly set to unlimited" from "never set,
	// falling through to a default that happens to be unlimited".
	Stored *adminStoredRow `json:"stored,omitempty"`
	// Defaults is what this deployment resolves to with no row at all — the
	// baseline a release returns the org to.
	Defaults entcore.Limits `json:"defaults"`
	// Audits is the org's entitlement change history, newest first.
	Audits []*models.OrgEntitlementAudit `json:"audits"`
}

// AdminWriteResponse is what the superadmin PUT / DELETE answer with.
type AdminWriteResponse struct {
	entcore.Resolved

	// Applied is false only in the (impossible for an admin write) case where
	// the precedence rule discarded the write; kept in the shape so the two
	// front doors report success identically.
	Applied bool `json:"applied"`
	// Released reports that a stored row was actually removed. False on a
	// release of an org that had no override — a no-op, not an error.
	Released bool `json:"released,omitempty"`
}

// AdminList handles GET /api/v1/system/entitlements — superadmin only.
//
// Search (`q`) and paging are applied to the ORG list first, so the expensive
// part (a resolve plus a checks-per-minute roll-up per org) only ever runs for
// the rows actually returned.
func (h *Handler) AdminList(writer http.ResponseWriter, req *http.Request) error {
	orgs, err := h.db.ListOrganizations(req.Context())
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	query := strings.ToLower(strings.TrimSpace(req.URL.Query().Get("q")))
	matched := make([]*models.Organization, 0, len(orgs))

	for _, org := range orgs {
		if query != "" &&
			!strings.Contains(strings.ToLower(org.Slug), query) &&
			!strings.Contains(strings.ToLower(org.Name), query) {
			continue
		}

		matched = append(matched, org)
	}

	sort.Slice(matched, func(i, j int) bool { return matched[i].Slug < matched[j].Slug })

	limit := parsePositiveInt(req.URL.Query().Get("limit"), defaultAdminPageSize, maxAdminPageSize)
	offset := parsePositiveInt(req.URL.Query().Get("offset"), 0, len(matched))

	page := matched
	if offset < len(page) {
		page = page[offset:]
	} else {
		page = nil
	}

	if len(page) > limit {
		page = page[:limit]
	}

	rows := make([]AdminOrgRow, 0, len(page))

	for _, org := range page {
		resolved, resolveErr := h.svc.Resolve(req.Context(), org.UID)
		if resolveErr != nil {
			return h.WriteInternalError(writer, req, resolveErr)
		}

		row := AdminOrgRow{
			OrganizationUID: org.UID,
			Slug:            org.Slug,
			Name:            org.Name,
			Resolved:        resolved,
		}

		if cpm, cpmErr := h.svc.ChecksPerMinuteStatus(req.Context(), org.UID); cpmErr == nil {
			row.ChecksPerMinute = &cpm
			row.OverCheckRate = cpm.Over()
		}

		if stored, storedErr := h.db.GetOrgEntitlements(req.Context(), org.UID); storedErr == nil &&
			stored != nil && stored.Payload.Source == models.EntitlementSourceAdmin {
			since := stored.UpdatedAt
			row.AdminOverrideSince = &since
		}

		rows = append(rows, row)
	}

	return h.WriteJSON(writer, http.StatusOK, AdminListResponse{Data: rows, Total: len(matched)})
}

// AdminGet handles GET /api/v1/system/entitlements/:org — superadmin only.
func (h *Handler) AdminGet(writer http.ResponseWriter, req *http.Request) error {
	org, err := h.lookupOrg(req)
	if err != nil || org == nil {
		return h.writeNotFound(writer)
	}

	resolved, err := h.svc.Resolve(req.Context(), org.UID)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	stored, err := h.db.GetOrgEntitlements(req.Context(), org.UID)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	audits, err := h.db.ListOrgEntitlementAudits(req.Context(), models.ListOrgEntitlementAuditsFilter{
		OrganizationUID: org.UID,
		Limit:           parsePositiveInt(req.URL.Query().Get("limit"), defaultAuditPageSize, maxAuditPageSize),
	})
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	detail := AdminOrgDetail{
		OrganizationUID: org.UID,
		Slug:            org.Slug,
		Name:            org.Name,
		Resolved:        resolved,
		Stored:          projectStoredRow(stored),
		Defaults:        h.svc.Defaults().Limits,
		Audits:          audits,
	}

	if cpm, cpmErr := h.svc.ChecksPerMinuteStatus(req.Context(), org.UID); cpmErr == nil {
		detail.ChecksPerMinute = &cpm
		detail.OverCheckRate = cpm.Over()
	}

	return h.WriteJSON(writer, http.StatusOK, detail)
}

// AdminPut handles PUT /api/v1/system/entitlements/:org — superadmin only.
//
// Whole-row only, by decision: the editor pre-fills from the resolved values
// and saves a complete admin-sourced row. The request's own `source` is
// ignored — a superadmin write is an admin write, and letting the body claim
// to be billing would be a way to launder around the precedence rule.
func (h *Handler) AdminPut(writer http.ResponseWriter, req *http.Request) error {
	org, err := h.lookupOrg(req)
	if err != nil || org == nil {
		return h.writeNotFound(writer)
	}

	var input entcore.Entitlements

	dec := json.NewDecoder(req.Body)
	dec.DisallowUnknownFields()

	if decErr := dec.Decode(&input); decErr != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: decErr.Error()},
		})
	}

	input.Source = models.EntitlementSourceAdmin

	outcome, err := h.svc.Apply(req.Context(), org.UID, input, h.adminActor(req), req.Header.Get("X-Entitlements-Reason"))
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	resolved, err := h.svc.Resolve(req.Context(), org.UID)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, AdminWriteResponse{
		Resolved: resolved,
		Applied:  outcome.Applied,
	})
}

// AdminRelease handles DELETE /api/v1/system/entitlements/:org — superadmin
// only. Drops the override so the next billing push (or the deployment
// defaults until then) drives the org's limits again.
func (h *Handler) AdminRelease(writer http.ResponseWriter, req *http.Request) error {
	org, err := h.lookupOrg(req)
	if err != nil || org == nil {
		return h.writeNotFound(writer)
	}

	released, err := h.svc.Release(
		req.Context(), org.UID, h.adminActor(req), req.Header.Get("X-Entitlements-Reason"),
	)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	resolved, err := h.svc.Resolve(req.Context(), org.UID)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, AdminWriteResponse{
		Resolved: resolved,
		Applied:  true,
		Released: released,
	})
}

// adminActor names the superadmin in the audit trail. The middleware has
// already proven the user is a superadmin, so an absent user here can only be
// a misconfigured route — attributed rather than crashed.
func (h *Handler) adminActor(req *http.Request) string {
	if user, ok := middleware.GetUserFromContext(req.Context()); ok {
		return "superadmin:" + user.UID
	}

	return "superadmin:unknown"
}

// projectStoredRow maps the bun model onto the API shape, or nil when the org
// has no stored row.
func projectStoredRow(row *models.OrgEntitlements) *adminStoredRow {
	if row == nil {
		return nil
	}

	return &adminStoredRow{
		Source:       row.Payload.Source,
		Limits:       row.Payload.Limits,
		DisplayName:  row.Payload.DisplayName,
		DisplayEmoji: row.Payload.DisplayEmoji,
		ExternalRef:  row.ExternalRef,
		ExpiresAt:    row.ExpiresAt,
		LastSyncedAt: row.LastSyncedAt,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

// parsePositiveInt reads a non-negative integer query parameter, falling back
// to def when absent or unparsable and clamping to maximum.
func parsePositiveInt(raw string, def, maximum int) int {
	if raw == "" {
		return def
	}

	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 0 {
		return def
	}

	if maximum > 0 && parsed > maximum {
		return maximum
	}

	return parsed
}

// RegisterAdminRoutes mounts the superadmin entitlements editor.
//
// It lives under /api/v1/system rather than /api/v1/orgs/:org on purpose: the
// org-scoped chain runs RequireOrgAccess, which would confine a superadmin to
// the org they happen to be signed into, and the whole point of this surface
// is editing SOMEBODY ELSE's limits. The gate is registered here, next to the
// handlers it protects, so one function owns both.
func RegisterAdminRoutes(api *httpx.Group, authMW *middleware.AuthMiddleware, handler *Handler) {
	group := api.NewGroup("/system/entitlements").
		Use(authMW.RequireAuth).
		Use(authMW.RequireSuperAdmin)
	group.GET("", handler.AdminList)
	group.GET("/:org", handler.AdminGet)
	group.PUT("/:org", handler.AdminPut)
	group.DELETE("/:org", handler.AdminRelease)
}
