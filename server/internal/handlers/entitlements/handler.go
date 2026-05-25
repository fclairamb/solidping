// Package entitlements wraps the entitlements service with HTTP handlers
// for GET / PUT / PATCH and the audit listing. The package owns its own
// auth gating: a service-token check (for the billing service) plus a
// fallback admin-user check (for self-hosted operators) gated by a
// system parameter.
package entitlements

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// System parameter keys driving auth + behavior.
const (
	ParamServiceToken       = "entitlements.service_token"
	ParamAdminWritesEnabled = "entitlements.admin_writes_enabled"
	ParamUpgradeURLTemplate = "entitlements.upgrade_url_template"
	ParamStaleAfterDays     = "entitlements.stale_after_days"
	defaultAuditPageSize    = 50
	maxAuditPageSize        = 200
)

// Handler exposes the entitlements API.
type Handler struct {
	base.HandlerBase
	svc *entcore.Service
	db  db.Service
}

// NewHandler builds the handler.
func NewHandler(svc *entcore.Service, dbService db.Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         svc,
		db:          dbService,
	}
}

// principal records who is making the call so the audit log can attribute it.
type principal struct {
	actor     string
	isService bool
	isAdmin   bool
}

// authorize accepts either a valid service token (preferred for SaaS) or
// an admin user JWT (gated by entitlements.admin_writes_enabled, default
// true in self-hosted, false in SaaS). Read-only endpoints accept any
// authenticated org member.
func (h *Handler) authorize(req bunrouter.Request, requireWrite bool) (*principal, error) {
	authHeader := req.Header.Get("Authorization")
	token := extractBearerToken(authHeader)

	if token != "" {
		expected, err := h.serviceToken(req.Context())
		if err == nil && expected != "" && constantTimeMatch(token, expected) {
			return &principal{actor: "service:entitlements", isService: true}, nil
		}
	}

	user, ok := middleware.GetUserFromContext(req.Context())
	if !ok {
		return nil, errUnauthorized
	}

	claims, _ := middleware.GetClaimsFromContext(req.Context())
	isAdmin := claims != nil && (claims.Role == "admin" || claims.Role == "superadmin")

	if requireWrite {
		writesEnabled, _ := h.adminWritesEnabled(req.Context())
		if !writesEnabled {
			return nil, errForbidden
		}
		if !isAdmin {
			return nil, errForbidden
		}
	}

	actor := "user:" + user.UID

	return &principal{actor: actor, isAdmin: isAdmin}, nil
}

var (
	errUnauthorized   = errors.New("authentication required")
	errForbidden      = errors.New("forbidden")
	errMissingOrgPath = errors.New("missing org path parameter")
)

// Get handles GET /api/v1/orgs/:org/entitlements.
func (h *Handler) Get(writer http.ResponseWriter, req bunrouter.Request) error {
	if _, err := h.authorize(req, false); err != nil {
		return h.writeAuthError(writer, err)
	}

	org, errResp := h.lookupOrg(req)
	if errResp != nil {
		return h.writeNotFound(writer)
	}

	resolved, err := h.svc.Resolve(req.Context(), org.UID)
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	upgradeURL, _ := h.upgradeURL(req.Context(), org.Slug)

	// Usage is opt-in via ?with=usage (comma-separated, consistent with the
	// checks endpoint's ?with=last_result,last_status_change). Without it,
	// no usage is computed — the cheap path stays cheap.
	var usagePtr *entcore.Usage
	if strings.Contains(req.URL.Query().Get("with"), "usage") {
		usage, usageErr := h.svc.Usage(req.Context(), org.UID)
		if usageErr != nil {
			return h.WriteInternalError(writer, usageErr)
		}
		usagePtr = &usage
	}

	return h.WriteJSON(writer, http.StatusOK, struct {
		entcore.Resolved
		Usage      *entcore.Usage `json:"usage,omitempty"`
		UpgradeURL string         `json:"upgradeUrl,omitempty"`
	}{Resolved: resolved, Usage: usagePtr, UpgradeURL: upgradeURL})
}

// Put handles PUT /api/v1/orgs/:org/entitlements — replaces the row.
func (h *Handler) Put(writer http.ResponseWriter, req bunrouter.Request) error {
	return h.write(writer, req, false)
}

// Patch handles PATCH /api/v1/orgs/:org/entitlements — partial update.
func (h *Handler) Patch(writer http.ResponseWriter, req bunrouter.Request) error {
	return h.write(writer, req, true)
}

func (h *Handler) write(writer http.ResponseWriter, req bunrouter.Request, partial bool) error {
	prin, err := h.authorize(req, true)
	if err != nil {
		return h.writeAuthError(writer, err)
	}

	org, errResp := h.lookupOrg(req)
	if errResp != nil {
		return h.writeNotFound(writer)
	}

	var input entcore.Entitlements
	dec := json.NewDecoder(req.Body)
	dec.DisallowUnknownFields()
	if decErr := dec.Decode(&input); decErr != nil {
		// DisallowUnknownFields rejects keys outside the modeled limits
		// (maxChecks / maxSsoUsers / maxChecksPerMinute) so typos surface
		// loudly instead of silently no-op-ing.
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: decErr.Error()},
		})
	}

	if input.Source == "" {
		if prin.isService {
			input.Source = models.EntitlementSourceBilling
		} else {
			input.Source = models.EntitlementSourceAdmin
		}
	}

	if partial {
		input = h.mergePartial(req.Context(), org.UID, input)
	}

	reason := req.Header.Get("X-Entitlements-Reason")

	if setErr := h.svc.Set(req.Context(), org.UID, input, prin.actor, reason); setErr != nil {
		return h.WriteInternalError(writer, setErr)
	}

	resolved, err := h.svc.Resolve(req.Context(), org.UID)
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resolved)
}

// ListAudits handles GET /api/v1/orgs/:org/entitlements/audits.
func (h *Handler) ListAudits(writer http.ResponseWriter, req bunrouter.Request) error {
	prin, err := h.authorize(req, false)
	if err != nil {
		return h.writeAuthError(writer, err)
	}
	if !prin.isService && !prin.isAdmin {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "Admin only")
	}

	org, errResp := h.lookupOrg(req)
	if errResp != nil {
		return h.writeNotFound(writer)
	}

	limit := defaultAuditPageSize
	if v := req.URL.Query().Get("limit"); v != "" {
		if parsed, parseErr := strconv.Atoi(v); parseErr == nil && parsed > 0 {
			limit = parsed
			if limit > maxAuditPageSize {
				limit = maxAuditPageSize
			}
		}
	}

	rows, err := h.db.ListOrgEntitlementAudits(req.Context(), models.ListOrgEntitlementAuditsFilter{
		OrganizationUID: org.UID,
		Limit:           limit,
	})
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, struct {
		Data []*models.OrgEntitlementAudit `json:"data"`
	}{Data: rows})
}

// mergePartial loads the current row and overlays it with the input.
func (h *Handler) mergePartial(
	ctx context.Context, orgUID string, input entcore.Entitlements,
) entcore.Entitlements {
	current, err := h.svc.Resolve(ctx, orgUID)
	if err != nil {
		slog.WarnContext(ctx, "patch merge: resolve failed; falling back to input as-is",
			"orgUID", orgUID, "error", err)

		return input
	}

	out := entcore.Entitlements{
		Limits:       current.Limits,
		Source:       input.Source,
		DisplayName:  current.DisplayName,
		ExpiresAt:    current.ExpiresAt,
		LastSyncedAt: current.LastSyncedAt,
	}

	overlayLimits(&out.Limits, input.Limits)

	if input.DisplayName != nil {
		out.DisplayName = input.DisplayName
	}
	if input.ExternalRef != nil {
		out.ExternalRef = input.ExternalRef
	}
	if input.Metadata != nil {
		out.Metadata = input.Metadata
	}
	if input.ExpiresAt != nil {
		out.ExpiresAt = input.ExpiresAt
	}
	if input.LastSyncedAt != nil {
		out.LastSyncedAt = input.LastSyncedAt
	}

	return out
}

func overlayLimits(dst *entcore.Limits, src entcore.Limits) {
	if src.MaxChecks != nil {
		dst.MaxChecks = src.MaxChecks
	}
	if src.MaxSSOUsers != nil {
		dst.MaxSSOUsers = src.MaxSSOUsers
	}
	if src.MaxChecksPerMinute != nil {
		dst.MaxChecksPerMinute = src.MaxChecksPerMinute
	}
}

// lookupOrg resolves :org from the route into a model.
func (h *Handler) lookupOrg(req bunrouter.Request) (*models.Organization, error) {
	slug := req.Param("org")
	if slug == "" {
		return nil, errMissingOrgPath
	}

	return h.db.GetOrganizationBySlug(req.Context(), slug)
}

func (h *Handler) writeAuthError(writer http.ResponseWriter, err error) error {
	if errors.Is(err, errForbidden) {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "Forbidden")
	}

	return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
}

func (h *Handler) writeNotFound(writer http.ResponseWriter) error {
	return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
}

func (h *Handler) serviceToken(ctx context.Context) (string, error) {
	param, err := h.db.GetSystemParameter(ctx, ParamServiceToken)
	if err != nil || param == nil {
		return "", err
	}

	if value, ok := param.Value["value"].(string); ok {
		return value, nil
	}

	return "", nil
}

func (h *Handler) adminWritesEnabled(ctx context.Context) (bool, error) {
	param, err := h.db.GetSystemParameter(ctx, ParamAdminWritesEnabled)
	if err != nil || param == nil {
		// Default true (self-hosted) when unset.
		return true, err
	}

	if value, ok := param.Value["value"].(bool); ok {
		return value, nil
	}
	if value, ok := param.Value["value"].(string); ok {
		parsed, parseErr := strconv.ParseBool(value)
		if parseErr != nil {
			// Treat unparsable string as "default true" — better to allow
			// admin writes than to silently lock them out.
			return true, nil //nolint:nilerr // documented fallback
		}
		return parsed, nil
	}

	return true, nil
}

func (h *Handler) upgradeURL(ctx context.Context, orgSlug string) (string, error) {
	param, err := h.db.GetSystemParameter(ctx, ParamUpgradeURLTemplate)
	if err != nil || param == nil {
		return "", err
	}

	template, ok := param.Value["value"].(string)
	if !ok || template == "" {
		return "", nil
	}

	return interpolateURL(template, orgSlug), nil
}

// interpolateURL replaces {org} with the slug. Lightweight by design — no
// general templating needed for one variable.
func interpolateURL(template, org string) string {
	return strings.ReplaceAll(template, "{org}", org)
}

func extractBearerToken(authHeader string) string {
	if authHeader == "" {
		return ""
	}

	const prefix = "Bearer "
	if len(authHeader) <= len(prefix) {
		return ""
	}
	if authHeader[:len(prefix)] != prefix && authHeader[:len(prefix)] != "bearer " {
		return ""
	}

	return authHeader[len(prefix):]
}

func constantTimeMatch(a, b string) bool {
	if len(a) != len(b) {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// FormatQuotaError translates an enforcement error into the JSON body the
// frontend expects. Used by the future enforcement-PR handlers.
func FormatQuotaError(err *entcore.QuotaError) map[string]any {
	return map[string]any{
		"limitName":    err.LimitName,
		"limit":        err.Limit,
		"currentUsage": err.CurrentUsage,
		"detail":       err.LimitName + " limit reached",
	}
}
