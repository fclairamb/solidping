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
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// System parameter keys driving auth + behavior.
const (
	// ParamServiceToken is the LEGACY static bearer the billing service used
	// to authenticate the entitlements push. Superseded by
	// ParamServiceSigningKeys; still accepted while
	// ParamAllowLegacyServiceToken is true (its default).
	ParamServiceToken = "entitlements.service_token"
	// ParamServiceSigningKeys holds the ordered {id, secret} set (newest
	// first, JSON array) used to VERIFY signed requests coming from the
	// billing service — the mirror of its BILLING_SIGNING_KEYS_OUTBOUND.
	ParamServiceSigningKeys = "entitlements.service_signing_keys"
	// ParamOutboundSigningKeys holds the ordered {id, secret} set used to
	// SIGN our own calls to the billing service's /api/v1/* endpoints — the
	// mirror of its BILLING_SIGNING_KEYS_INBOUND. A separate set per
	// direction, so a leak of one cannot forge the other.
	ParamOutboundSigningKeys = "entitlements.outbound_signing_keys"
	// ParamAllowLegacyServiceToken gates acceptance of ParamServiceToken.
	// Defaults to true: it must stay on until the billing service has
	// stopped sending the static bearer, and turning it off is a parameter
	// flip, not a deploy.
	ParamAllowLegacyServiceToken = "entitlements.allow_legacy_service_token"
	ParamAdminWritesEnabled      = "entitlements.admin_writes_enabled"
	ParamUpgradeURLTemplate      = "entitlements.upgrade_url_template"
	// ParamBillingInboundSecret is the shared BEARER credential presented on
	// service calls between this instance and the billing service. Set to the
	// same value as the billing service's BILLING_INBOUND_SECRET. It is NOT
	// the upgrade-token signing key any more — see
	// ParamBillingUpgradeTokenSecret — though it is still accepted as the
	// signing key while that parameter is unset, so an unconfigured install
	// keeps working.
	ParamBillingInboundSecret = "entitlements.billing_inbound_secret"
	// ParamBillingUpgradeTokenSecret is the HS256 key that signs the `#bt=`
	// upgrade token. It is deliberately NOT ParamBillingInboundSecret: that
	// one is a bearer credential sent on every service call, and a leak of a
	// bearer must not also be the power to mint an upgrade token for any org.
	// Collapsing the two back into one value is a security regression, not a
	// simplification. Mirrors the billing service's
	// BILLING_UPGRADE_TOKEN_SECRET.
	ParamBillingUpgradeTokenSecret = "entitlements.billing_upgrade_token_secret"
	ParamStaleAfterDays            = "entitlements.stale_after_days"
	// billingTokenTTL is how long a minted billing upgrade token is valid.
	billingTokenTTL      = time.Hour
	defaultAuditPageSize = 50
	maxAuditPageSize     = 200
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

// principal records who is making the call so the audit log can attribute it,
// and — since spec 2026-08-26-06 — so the write path can decide WHICH source a
// user-driven write is allowed to mint.
type principal struct {
	actor     string
	isService bool
	isAdmin   bool
	// isSuperAdmin is what separates an instance operator from an org admin.
	// Only a superadmin may mint models.EntitlementSourceAdmin, the source that
	// outranks billing until explicitly released.
	isSuperAdmin bool
}

// authorize accepts a trusted service caller (preferred for SaaS) or an admin
// user JWT (gated by entitlements.admin_writes_enabled, default true in
// self-hosted, false in SaaS). Read-only endpoints accept any authenticated
// org member.
//
// A service caller is recognized in two ways, in order:
//
//  1. middleware.ServiceSignature already verified an X-SP-Signature over this
//     request — the supported path.
//  2. LEGACY: the raw bearer matches entitlements.service_token, and
//     entitlements.allow_legacy_service_token is still on. This duplicate of
//     the middleware check exists because the handler is also mounted without
//     the middleware in tests; it honors the same flag so the flag cannot be
//     bypassed by going through the handler.
func (h *Handler) authorize(req *http.Request, requireWrite bool) (*principal, error) {
	if middleware.IsServiceAuthorized(req.Context()) {
		return &principal{actor: "service:entitlements", isService: true}, nil
	}

	if token := extractBearerToken(req.Header.Get("Authorization")); token != "" {
		expected, err := h.serviceToken(req.Context())
		if err == nil && expected != "" && constantTimeMatch(token, expected) {
			if h.legacyServiceTokenAllowed(req.Context()) {
				slog.WarnContext(req.Context(),
					"DEPRECATED: entitlements request authorized by the static service token; "+
						"the caller should sign it instead (X-SP-Signature)",
					"path", req.URL.Path, "method", req.Method)

				return &principal{actor: "service:entitlements", isService: true}, nil
			}

			slog.WarnContext(req.Context(),
				"legacy entitlements service token presented but disabled",
				"path", req.URL.Path, "param", ParamAllowLegacyServiceToken)
		}
	}

	user, ok := middleware.GetUserFromContext(req.Context())
	if !ok {
		return nil, errUnauthorized
	}

	claims, _ := middleware.GetClaimsFromContext(req.Context())
	isAdmin := claims.HasOrgRole(models.MemberRoleAdmin)

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

	return &principal{actor: actor, isAdmin: isAdmin, isSuperAdmin: user.SuperAdmin}, nil
}

var (
	errUnauthorized   = errors.New("authentication required")
	errForbidden      = errors.New("forbidden")
	errMissingOrgPath = errors.New("missing org path parameter")
)

// Get handles GET /api/v1/orgs/:org/entitlements.
func (h *Handler) Get(writer http.ResponseWriter, req *http.Request) error {
	prin, err := h.authorize(req, false)
	if err != nil {
		return h.writeAuthError(writer, err)
	}

	org, errResp := h.lookupOrg(req)
	if errResp != nil {
		return h.writeNotFound(writer)
	}

	resolved, err := h.svc.Resolve(req.Context(), org.UID)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	// The upgrade URL is only meaningful to an org admin: it carries a signed
	// billing token that lets the billing service act on the admin's behalf.
	// Non-admins (and the billing service principal itself) get no upgradeUrl —
	// the dashboard only renders the Upgrade button when it is present.
	var upgradeURL string
	if prin.isAdmin {
		upgradeURL, _ = h.adminUpgradeURL(req.Context(), org.Slug)
	}

	// Usage is opt-in via ?with=usage (comma-separated, consistent with the
	// checks endpoint's ?with=last_result,last_status_change). Without it,
	// no usage is computed — the cheap path stays cheap.
	var usagePtr *entcore.Usage
	if strings.Contains(req.URL.Query().Get("with"), "usage") {
		usage, usageErr := h.svc.Usage(req.Context(), org.UID)
		if usageErr != nil {
			return h.WriteInternalError(writer, req, usageErr)
		}
		usagePtr = &usage
	}

	// checksPerMinute is NOT behind ?with=usage: the over-limit banner it drives
	// has to render on the checks list too, and that page must not have to pay
	// for the full usage roll-up (member counts, agents, custom domains, SLOs)
	// to learn it is being throttled. Two cheap queries, on an endpoint the
	// dashboard caches for a minute.
	//
	// It fails soft for the same reason: an over-limit warning is worth less
	// than the limits payload itself, so a failure here must not 500 the page
	// that renders the plan.
	var cpmPtr *entcore.ChecksPerMinute
	if cpm, cpmErr := h.svc.ChecksPerMinuteStatus(req.Context(), org.UID); cpmErr != nil {
		slog.WarnContext(req.Context(), "checks-per-minute status failed; omitting it from the payload",
			"orgUID", org.UID, "error", cpmErr)
	} else {
		cpmPtr = &cpm
	}

	return h.WriteJSON(writer, http.StatusOK, struct {
		entcore.Resolved
		Usage           *entcore.Usage           `json:"usage,omitempty"`
		ChecksPerMinute *entcore.ChecksPerMinute `json:"checksPerMinute,omitempty"`
		UpgradeURL      string                   `json:"upgradeUrl,omitempty"`
	}{Resolved: resolved, Usage: usagePtr, ChecksPerMinute: cpmPtr, UpgradeURL: upgradeURL})
}

// Put handles PUT /api/v1/orgs/:org/entitlements — replaces the row.
func (h *Handler) Put(writer http.ResponseWriter, req *http.Request) error {
	return h.write(writer, req, false)
}

// Patch handles PATCH /api/v1/orgs/:org/entitlements — partial update.
func (h *Handler) Patch(writer http.ResponseWriter, req *http.Request) error {
	return h.write(writer, req, true)
}

func (h *Handler) write(writer http.ResponseWriter, req *http.Request, partial bool) error {
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
		// (maxChecks / maxUsers / maxChecksPerMinute; maxSsoUsers is
		// accepted as a deprecated alias for maxUsers) so typos surface
		// loudly instead of silently no-op-ing. Sending both maxUsers and
		// maxSsoUsers is likewise rejected (ErrConflictingUserLimitKeys).
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: decErr.Error()},
		})
	}

	input.Source = h.sourceFor(prin, input.Source)

	if partial {
		input = h.mergePartial(req.Context(), org.UID, input)
	}

	reason := req.Header.Get("X-Entitlements-Reason")

	// Apply, not Set: the precedence rule lives in the service so both front
	// doors (this one and the superadmin editor) obey it. A billing push onto
	// an admin override answers 200 with applied=false rather than an error —
	// billing must not error-loop over a decision we made on purpose — and the
	// body says exactly what happened.
	outcome, applyErr := h.svc.Apply(req.Context(), org.UID, input, prin.actor, reason)
	if applyErr != nil {
		return h.WriteInternalError(writer, req, applyErr)
	}

	resolved, err := h.svc.Resolve(req.Context(), org.UID)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, newWriteResponse(resolved, outcome))
}

// sourceFor decides the source a write is recorded under. It is a decision the
// SERVER makes, never one the body gets to assert, because since spec
// 2026-08-26-06 the source is an authorization outcome and not a label:
// `admin` suppresses every subsequent billing push until a superadmin releases
// it, so a caller that could name its own source could mint an unreleasable
// override for itself.
//
//   - A trusted service keeps the old behavior (it may name a source; absent,
//     it is the billing service). Nothing else can reach that branch: the
//     signature/bypass middleware is what sets it.
//   - A superadmin on this org-scoped route mints `admin` — same authority as
//     the dedicated superadmin editor, reached through a different URL.
//   - Everyone else — including an org OWNER — mints `org-admin`: a real,
//     paid-tier provisioning row that billing's next reconcile still corrects.
//     That is exactly what this door did before the precedence rule existed.
func (h *Handler) sourceFor(prin *principal, requested models.EntitlementSource) models.EntitlementSource {
	if prin.isService {
		if requested == "" {
			return models.EntitlementSourceBilling
		}

		return requested
	}

	if prin.isSuperAdmin {
		return models.EntitlementSourceAdmin
	}

	return models.EntitlementSourceOrgAdmin
}

// writeResponse is the entitlements write payload: the resolved row plus an
// honest report of whether this particular write changed anything.
type writeResponse struct {
	entcore.Resolved
	// Applied is false when the precedence rule discarded the write.
	Applied bool `json:"applied"`
	// SuppressedBy names the source that won, when Applied is false.
	SuppressedBy models.EntitlementSource `json:"suppressedBy,omitempty"`
	// Message explains a suppression in words, for a log line or a UI toast.
	Message string `json:"message,omitempty"`
}

//nolint:gocritic // resolved is the wire shape, passed by value like everywhere else
func newWriteResponse(resolved entcore.Resolved, outcome entcore.WriteOutcome) writeResponse {
	out := writeResponse{Resolved: resolved, Applied: outcome.Applied}
	if !outcome.Applied {
		out.SuppressedBy = outcome.SuppressedBy
		out.Message = entcore.SuppressedByAdminMessage
	}

	return out
}

// ListAudits handles GET /api/v1/orgs/:org/entitlements/audits.
func (h *Handler) ListAudits(writer http.ResponseWriter, req *http.Request) error {
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
		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, struct {
		Data []*models.OrgEntitlementAudit `json:"data"`
	}{Data: rows})
}

// mergePartial loads the current row and overlays it with the input.
//
//nolint:gocritic // input is the decoded wire shape, passed by value to match the API contract
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
		DisplayEmoji: current.DisplayEmoji,
		ExpiresAt:    current.ExpiresAt,
		LastSyncedAt: current.LastSyncedAt,
	}

	overlayLimits(&out.Limits, &input.Limits)

	if input.DisplayName != nil {
		out.DisplayName = input.DisplayName
	}
	if input.DisplayEmoji != nil {
		out.DisplayEmoji = input.DisplayEmoji
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

func overlayLimits(dst *entcore.Limits, src *entcore.Limits) {
	if src.MaxChecks != nil {
		dst.MaxChecks = src.MaxChecks
	}
	if src.MaxUsers != nil {
		dst.MaxUsers = src.MaxUsers
	}
	if src.MaxChecksPerMinute != nil {
		dst.MaxChecksPerMinute = src.MaxChecksPerMinute
	}
	if src.MaxDeportedAgents != nil {
		dst.MaxDeportedAgents = src.MaxDeportedAgents
	}
	if src.MaxCustomDomains != nil {
		dst.MaxCustomDomains = src.MaxCustomDomains
	}
	if src.MaxSmsPerMonth != nil {
		dst.MaxSmsPerMonth = src.MaxSmsPerMonth
	}
	if src.MaxCallsPerMonth != nil {
		dst.MaxCallsPerMonth = src.MaxCallsPerMonth
	}
	if src.MaxWhatsappPerMonth != nil {
		dst.MaxWhatsappPerMonth = src.MaxWhatsappPerMonth
	}
	if src.MaxSlos != nil {
		dst.MaxSlos = src.MaxSlos
	}
	if src.WhiteLabel != nil {
		dst.WhiteLabel = src.WhiteLabel
	}
}

// lookupOrg resolves :org from the route into a model.
func (h *Handler) lookupOrg(req *http.Request) (*models.Organization, error) {
	slug := httpx.Param(req, "org")
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
	// Default true (self-hosted) when unset or unparsable — better to allow
	// admin writes than to silently lock an operator out.
	return h.boolParam(ctx, ParamAdminWritesEnabled, true)
}

// legacyServiceTokenAllowed reports whether the deprecated static bearer is
// still accepted. Defaults to TRUE when unset: the billing service keeps
// sending it until the cross-repo migration completes, and flipping this off
// early breaks every entitlements push.
func (h *Handler) legacyServiceTokenAllowed(ctx context.Context) bool {
	allowed, _ := h.boolParam(ctx, ParamAllowLegacyServiceToken, true)

	return allowed
}

// boolParam reads a boolean system parameter, accepting both a JSON bool and a
// stringly-typed "true"/"false" (both shapes occur depending on how the value
// was seeded), and falling back to def when absent or unparsable.
func (h *Handler) boolParam(ctx context.Context, key string, def bool) (bool, error) {
	param, err := h.db.GetSystemParameter(ctx, key)
	if err != nil || param == nil {
		return def, err
	}

	switch value := param.Value["value"].(type) {
	case bool:
		return value, nil
	case string:
		parsed, parseErr := strconv.ParseBool(value)
		if parseErr != nil {
			return def, nil //nolint:nilerr // documented fallback
		}

		return parsed, nil
	default:
		return def, nil
	}
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

// adminUpgradeURL builds the upgrade URL for an org admin and, when an
// upgrade-token secret is configured, appends a signed billing token as a URL
// fragment (`#bt=<token>`). Fragments are never sent to servers, so the token
// can't leak via Referer headers or access logs. Callers must have already
// verified the principal is an org admin.
func (h *Handler) adminUpgradeURL(ctx context.Context, orgSlug string) (string, error) {
	upgradeURL, err := h.upgradeURL(ctx, orgSlug)
	if err != nil || upgradeURL == "" {
		return upgradeURL, err
	}

	secret, viaFallback, secretErr := h.upgradeTokenSecret(ctx)
	if secretErr != nil || secret == "" {
		// No billing secret configured (self-hosted without billing) — return
		// the plain upgrade URL with no token fragment.
		return upgradeURL, secretErr
	}

	if viaFallback {
		warnUpgradeTokenFallback(ctx)
	}

	user, ok := middleware.GetUserFromContext(ctx)
	if !ok {
		return upgradeURL, nil
	}

	token, tokenErr := mintBillingToken(secret, orgSlug, user.UID, user.Email)
	if tokenErr != nil {
		slog.WarnContext(ctx, "failed to mint billing upgrade token; returning URL without fragment",
			"orgSlug", orgSlug, "error", tokenErr)

		return upgradeURL, nil
	}

	return upgradeURL + "#bt=" + token, nil
}

// warnUpgradeTokenFallbackOnce keeps the deprecation WARN to one line per
// process: adminUpgradeURL runs on a dashboard read path, so logging per URL
// build would be pure noise.
var warnUpgradeTokenFallbackOnce sync.Once //nolint:gochecknoglobals // process-wide one-shot log guard

// warnUpgradeTokenFallback emits the deprecation warning at most once per
// process. It mirrors the billing service's own fallback-acceptance message so
// the two logs read as one migration.
func warnUpgradeTokenFallback(ctx context.Context) {
	warnUpgradeTokenFallbackOnce.Do(func() {
		slog.WarnContext(ctx, "minting the billing upgrade token with the deprecated "+
			ParamBillingInboundSecret+" fallback; set "+ParamBillingUpgradeTokenSecret+
			" (SP_ENTITLEMENTS_BILLING_UPGRADE_TOKEN_SECRET) to a dedicated value, then set "+
			"BILLING_ALLOW_LEGACY_UPGRADE_TOKEN_SECRET=false on the billing service",
			"missingParam", ParamBillingUpgradeTokenSecret)
	})
}

// upgradeTokenSecret resolves the HS256 key used to sign the `#bt=` upgrade
// token: the dedicated ParamBillingUpgradeTokenSecret when set, otherwise the
// legacy ParamBillingInboundSecret bearer. viaFallback reports that the legacy
// bearer produced the secret — the operator-visible signal that the split of
// the two credentials is still pending. Empty secret means no billing is
// configured at all (self-hosted).
func (h *Handler) upgradeTokenSecret(ctx context.Context) (string, bool, error) {
	dedicated, err := h.systemParameterString(ctx, ParamBillingUpgradeTokenSecret)
	if err != nil {
		return "", false, err
	}

	if dedicated != "" {
		return dedicated, false, nil
	}

	legacy, err := h.billingInboundSecret(ctx)
	if err != nil {
		return "", false, err
	}

	return legacy, legacy != "", nil
}

// billingInboundSecret reads the shared bearer credential, which doubles as
// the upgrade-token signing key while ParamBillingUpgradeTokenSecret is unset.
// Empty when unset (self-hosted without billing).
func (h *Handler) billingInboundSecret(ctx context.Context) (string, error) {
	return h.systemParameterString(ctx, ParamBillingInboundSecret)
}

// systemParameterString reads a string-valued system parameter, returning ""
// when the parameter is absent or not a string.
func (h *Handler) systemParameterString(ctx context.Context, key string) (string, error) {
	param, err := h.db.GetSystemParameter(ctx, key)
	if err != nil || param == nil {
		return "", err
	}

	if value, ok := param.Value["value"].(string); ok {
		return value, nil
	}

	return "", nil
}

// billingClaims is the payload of the signed billing upgrade token. The
// billing service verifies it statelessly (signature, expiry, purpose, org).
type billingClaims struct {
	Purpose string `json:"purpose"`
	Org     string `json:"org"`
	Email   string `json:"email"`
	jwt.RegisteredClaims
}

// mintBillingToken signs an HS256 JWT authorizing the given user to drive
// billing changes for org. Claims: purpose=billing, org, sub=<user uid>,
// email, iat, exp=iat+1h.
func mintBillingToken(secret, orgSlug, userUID, email string) (string, error) {
	now := time.Now()
	claims := billingClaims{
		Purpose: "billing",
		Org:     orgSlug,
		Email:   email,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userUID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(billingTokenTTL)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	return token.SignedString([]byte(secret))
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
