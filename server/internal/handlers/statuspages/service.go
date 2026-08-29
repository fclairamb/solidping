// Package statuspages provides HTTP handlers for status page management endpoints.
package statuspages

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/analytics"
	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sloghook"
	"github.com/fclairamb/solidping/server/internal/domainverify"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/badges"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/statuspageassets"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/statuspagekiosk"
	"github.com/fclairamb/solidping/server/internal/statuspagelock"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// slugRegex validates slug format: lowercase letter, then 2-99 lowercase letters/digits/hyphens.
var slugRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{2,99}$`)

// slugInvalidCharsRegex matches every character a slug may not contain.
var slugInvalidCharsRegex = regexp.MustCompile(`[^a-z0-9-]`)

const (
	// minSlugLen and maxSlugLen mirror slugRegex, which is itself kept in step
	// with the `status_pages_slug_check` / `status_page_sections_slug_check`
	// database constraints (migration 009).
	minSlugLen = 3
	maxSlugLen = 100

	// maxSlugSuffix bounds the -2, -3, ... disambiguation walk for generated
	// slugs. Past this we would rather surface a conflict than keep probing.
	maxSlugSuffix = 99

	// Fallbacks for names that normalize away entirely ("###", "…", "ok").
	// Both satisfy slugRegex by construction, which is what makes
	// generateSlug total.
	slugFallbackPage    = "status-page"
	slugFallbackSection = "section"

	// defaultSectionName / defaultSectionSlug name the section every new
	// status page is seeded with (spec 2026-08-28-16). The slug is a fixed
	// literal, not generated: it's always the first section on a brand-new
	// page, so there is nothing to collide with yet.
	defaultSectionName = "Services"
	defaultSectionSlug = "services"
)

// generateSlug derives a slug from a display name. It is TOTAL: every input,
// including "" and strings made purely of punctuation, yields a string that
// satisfies slugRegex — see TestGenerateSlugAlwaysValid.
//
// That totality is the point. Both create paths used to pass an absent slug
// straight through to the insert, where the CHECK constraint rejected it and
// the driver error surfaced as a 500 with a raw SQLSTATE in the body.
func generateSlug(name, fallback string) string {
	slug := strings.ToLower(name)
	slug = slugInvalidCharsRegex.ReplaceAllString(slug, "-")

	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}

	slug = strings.Trim(slug, "-")

	// A slug has to START with a letter. Trimming the leading digits reads
	// better than prefixing a filler character would ("2024 Recap" becomes
	// "recap", not "x2024-recap").
	slug = strings.TrimLeft(slug, "0123456789-")

	if len(slug) > maxSlugLen {
		slug = strings.TrimRight(slug[:maxSlugLen], "-")
	}

	if len(slug) < minSlugLen {
		return fallback
	}

	return slug
}

// suffixSlug appends "-<n>", shortening the base first so the result still
// fits maxSlugLen. The base stays >= minSlugLen because the longest suffix
// this is called with is 3 characters against a 100-character budget.
func suffixSlug(base string, n int) string {
	suffix := "-" + strconv.Itoa(n)

	if len(base)+len(suffix) > maxSlugLen {
		base = strings.TrimRight(base[:maxSlugLen-len(suffix)], "-")
	}

	return base + suffix
}

// slugTaken reports whether a candidate slug is already used within a scope.
type slugTaken func(ctx context.Context, slug string) (bool, error)

// resolveGeneratedSlug walks base, base-2, base-3, ... until one is free.
// Only ever used for slugs WE generated: a slug the caller supplied must
// collide loudly (ErrSlugConflict) rather than be silently renamed.
func resolveGeneratedSlug(ctx context.Context, base string, taken slugTaken) (string, error) {
	slug := base

	for i := 2; i <= maxSlugSuffix+1; i++ {
		used, err := taken(ctx, slug)
		if err != nil {
			return "", err
		}

		if !used {
			return slug, nil
		}

		slug = suffixSlug(base, i)
	}

	return "", ErrSlugConflict
}

// resolveCreateSlug settles the slug for a newly created page or section.
//
// The two branches deliberately differ on collision. A slug the caller typed
// is part of their request, so a clash is an error they need to see
// (ErrSlugConflict); silently storing something else would be a surprise. A
// slug we generated carries no such intent, so it is disambiguated with a
// numeric suffix instead.
//
// Only a wholly absent slug ("") is generated. Whitespace ("   ") still fails
// validation as it does today — it is far more likely a mistake than an
// intent to auto-name, and turning it into a silent success would hide that.
func (s *Service) resolveCreateSlug(
	ctx context.Context, requested, name, fallback string, taken slugTaken,
) (string, error) {
	if requested == "" {
		return resolveGeneratedSlug(ctx, generateSlug(name, fallback), taken)
	}

	if err := validateSlug(requested); err != nil {
		return "", err
	}

	used, err := taken(ctx, requested)
	if err != nil {
		return "", err
	}

	if used {
		return "", ErrSlugConflict
	}

	return requested, nil
}

var (
	// ErrOrganizationNotFound is returned when an organization is not found.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrStatusPageNotFound is returned when a status page is not found.
	ErrStatusPageNotFound = errors.New("status page not found")
	// ErrStatusPageSectionNotFound is returned when a section is not found.
	ErrStatusPageSectionNotFound = errors.New("status page section not found")
	// ErrCheckNotFound is returned when a check is not found.
	ErrCheckNotFound = errors.New("check not found")
	// ErrCheckUIDInvalid is returned when an entry in a create-status-page
	// request's checkUids does not resolve to a check in the org. Always
	// wrapped with the offending UID via fmt.Errorf("%w: %s", ...) so
	// err.Error() names it, mirroring ErrSettingsUnknownField.
	ErrCheckUIDInvalid = errors.New("checkUids contains an unknown check uid")
	// ErrCheckGroupNotFound is returned when a check group is not found.
	ErrCheckGroupNotFound = errors.New("check group not found")
	// ErrResourceTargetInvalid is returned when a resource write sets zero or
	// both of checkUid / checkGroupUid. A resource targets exactly one of them
	// (spec 2026-08-01-03), mirroring the database's XOR check constraint.
	ErrResourceTargetInvalid = errors.New("exactly one of checkUid or checkGroupUid must be set")
	// ErrSlugConflict is returned when a slug already exists.
	ErrSlugConflict = errors.New("slug already exists")
	// ErrInvalidSlugFormat is returned when a slug has an invalid format.
	ErrInvalidSlugFormat = errors.New("invalid slug format")
	// ErrInvalidHistoryPeriod is returned when historyPeriod is not one of the
	// supported enum values (24h, 7d, 30d, 90d).
	ErrInvalidHistoryPeriod = errors.New("invalid history period")
	// ErrInvalidAutoResolve is returned when autoResolve is not one of
	// always | if_untouched | never.
	ErrInvalidAutoResolve = errors.New("invalid auto-resolve policy")
	// ErrInvalidAutoPublishDelay is returned when autoPublishDelaySeconds is
	// outside 0–86400. Zero is legal and means "publish immediately".
	ErrInvalidAutoPublishDelay = errors.New("auto-publish delay must be between 0 and 86400 seconds")
	// ErrReorderUIDsMismatch is returned when a reorder request's UID list
	// does not exactly match the section's current resources (missing,
	// extra, or duplicate UIDs).
	ErrReorderUIDsMismatch = errors.New("reorder uids do not match the section's resources")
	// ErrCustomCSSTooLarge is returned when customCss exceeds MaxCustomCSSBytes.
	ErrCustomCSSTooLarge = errors.New("custom css is too large")
	// ErrCustomCSSImport is returned when customCss contains an @import rule.
	ErrCustomCSSImport = errors.New("custom css must not contain @import")
	// ErrInvalidAvailabilityThresholds is returned when the effective
	// (submitted or defaulted) availability thresholds don't satisfy
	// 0 < thresholdDegraded < thresholdUp <= 100.
	ErrInvalidAvailabilityThresholds = errors.New(
		"thresholdDegraded must be greater than 0, less than thresholdUp, and thresholdUp must be at most 100")
	// ErrSettingsUnknownField is returned when the settings object (or a
	// section within it) contains a key that doesn't match the typed struct —
	// DisallowUnknownFields catches typos instead of silently dropping them.
	ErrSettingsUnknownField = errors.New("settings contains an unknown field")
)

// MaxCustomCSSBytes caps a status page's custom stylesheet. 64 KB is far more
// than a re-theme needs (the whole status0 variable set is a few hundred
// bytes) while keeping the value cheap to store and to ship on every public
// page render.
const MaxCustomCSSBytes = 64 * 1024

// validateCustomCSS enforces the two write-side rules on a custom stylesheet:
// a 64 KB cap, and no @import — which would let the page chain arbitrary
// third-party stylesheets (and, through them, fetch-on-render URLs) beyond
// what the operator actually reviewed. External url() references (web fonts,
// background images) stay deliberately allowed: the page admin is trusted for
// their own public page, and blocking them would make the feature useless.
// A nil pointer (field omitted) is valid.
func validateCustomCSS(css *string) error {
	if css == nil {
		return nil
	}

	if len(*css) > MaxCustomCSSBytes {
		return ErrCustomCSSTooLarge
	}

	// Case-insensitive, position-independent: "@IMPORT", a mid-file
	// "\n  @import", and an in-comment one are all rejected. Being stricter
	// than the CSS grammar is intentional — there is no legitimate reason for
	// the substring to appear in a status-page theme.
	if strings.Contains(strings.ToLower(*css), "@import") {
		return ErrCustomCSSImport
	}

	return nil
}

// validateAvailabilitySettings validates the EFFECTIVE (submitted-or-default)
// availability thresholds: 0 < thresholdDegraded < thresholdUp <= 100. A nil
// section resolves to the defaults, which always pass — this is the positive
// control that defaults are never invalid.
func validateAvailabilitySettings(a *models.AvailabilitySettings) error {
	up, degraded := a.EffectiveThresholds()

	if !(degraded > 0 && degraded < up && up <= 100) {
		return ErrInvalidAvailabilityThresholds
	}

	return nil
}

// resolveSettingsUpdate applies the no-deep-merge PATCH semantics for
// status_pages.settings against the page's current value:
//   - req not mentioning "settings" at all (SettingsPresent=false) ⇒ nil,
//     nil: the caller must leave the column untouched.
//   - "settings" present but the "availability" key absent from it ⇒ the
//     section is left as current (sections not mentioned are untouched).
//   - "availability" present as an object ⇒ replaces the section wholly
//     (not a field-by-field merge).
//   - "availability" present as explicit null ⇒ resets the section to
//     defaults (nil).
//
// A top-level explicit `"settings": null` (Settings==nil but
// SettingsPresent==true) has no sections to inspect, so it is a no-op —
// consistent with "sections not mentioned are untouched" since there are no
// section keys to mention.
func resolveSettingsUpdate(
	current models.StatusPageSettings, req *UpdateStatusPageRequest,
) (*models.StatusPageSettings, error) {
	if !req.SettingsPresent {
		return nil, nil //nolint:nilnil // nil,nil is the documented "leave untouched" sentinel
	}

	next := current

	if req.Settings != nil && req.Settings.AvailabilitySet {
		if req.Settings.Availability == nil {
			next.Availability = nil
		} else {
			next.Availability = &models.AvailabilitySettings{
				ThresholdUp:       req.Settings.Availability.ThresholdUp,
				ThresholdDegraded: req.Settings.Availability.ThresholdDegraded,
			}
		}
	}

	if err := validateAvailabilitySettings(next.Availability); err != nil {
		return nil, err
	}

	return &next, nil
}

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

// validateHistoryPeriod rejects a non-nil historyPeriod that is not one of the
// supported enum values. A nil pointer (field omitted) is valid.
func validateHistoryPeriod(period *string) error {
	if period == nil {
		return nil
	}

	if !models.StatusPagePeriod(*period).Valid() {
		return ErrInvalidHistoryPeriod
	}

	return nil
}

// maxAutoPublishDelaySeconds bounds the debounce at a day. A longer delay is
// indistinguishable from "never publish", which autoPublish=false already says.
const maxAutoPublishDelaySeconds = 86400

// validateAutoPublishSettings rejects an unknown auto-resolve policy or an
// out-of-range debounce. Nil pointers (fields omitted) are valid.
func validateAutoPublishSettings(autoResolve *string, delaySeconds *int) error {
	if autoResolve != nil && !models.AutoResolvePolicy(*autoResolve).IsValid() {
		return ErrInvalidAutoResolve
	}

	if delaySeconds != nil && (*delaySeconds < 0 || *delaySeconds > maxAutoPublishDelaySeconds) {
		return ErrInvalidAutoPublishDelay
	}

	return nil
}

// Service provides business logic for status page management.
type Service struct {
	db  db.Service
	cfg *config.Config
	// ent gates MaxCustomDomains when setting a new custom domain. nil disables
	// the quota check (e.g. the MCP handler, which never sets domains).
	ent *entitlements.Service
	// verifier runs the custom-domain DNS checks. Injectable for tests.
	verifier *domainverify.Verifier
	// unlockLimiter throttles password-page unlock attempts per (IP, page).
	unlockLimiter *unlockRateLimiter
	// verifyLimiter throttles the synchronous verify-now endpoint per org.
	verifyLimiter *verifyRateLimiter
	// certStatus reports the in-server-ACME certificate state for a custom
	// domain. nil when in-server TLS is off (self-hosted behind an external
	// proxy, or acme.enabled = false), in which case the response simply omits
	// customDomainCertStatus.
	certStatus CertStatusProvider
	// forgetDomain drops TLS state for a domain that stopped being ours. Same
	// object as certStatus when the edge is running; nil otherwise.
	forgetDomain DomainForgetter
	// analytics receives the status_page_published product event. Injectable
	// for tests: nil means "use the process-wide client" (the no-op unless
	// PostHog is configured), while a test attaches its OWN recorder here.
	//
	// This is deliberately per-Service rather than a swapped-out global. Every
	// test that creates or updates a status page goes through a capture path,
	// so a shared mutable global made any two such tests collide under
	// t.Parallel() — one test's events landing in another's assertions. Per
	// spec 2026-08-02-08's own convention, that is a bug to design out, not a
	// flake to re-run past.
	analytics analytics.Client
	// publicIncidents supplies the activeIncidents[] block on the public view.
	// nil = the feature is not wired (tests, or a build without status-page
	// publications), in which case the field is simply omitted.
	publicIncidents PublicIncidentProvider
	// jobs queues the operator email sent when a custom domain is hard-demoted
	// by the synchronous Verify button. nil (the MCP handler, most tests) still
	// records the audit event — only the mail is skipped.
	jobs jobsvc.Service
}

// SetJobsService wires the job queue used to deliver the custom-domain
// demotion alert. Optional: without it the demotion is still recorded as an
// audit event, it simply reaches nobody's inbox.
func (s *Service) SetJobsService(jobs jobsvc.Service) {
	s.jobs = jobs
}

// SetPublicIncidentProvider wires the incident-publication projection into the
// public page view. Optional.
func (s *Service) SetPublicIncidentProvider(p PublicIncidentProvider) {
	s.publicIncidents = p
}

// CertStatusProvider reports the TLS certificate state of a custom domain so
// the dashboard can explain why HTTPS is not live yet. Implemented by
// internal/tlsedge; injected via Service.SetCertStatusProvider to keep this
// package free of a certmagic dependency.
type CertStatusProvider interface {
	// CertStatus returns "issued", "error" or "none" for a (normalized) domain.
	CertStatus(domain string) string
}

// DomainForgetter drops every trace of a hostname the instance no longer
// serves — the cached certificate, its private key and the memoized "this host
// is ours" answer. Implemented by internal/tlsedge.
//
// This is hygiene, not the security boundary: the edge refuses to serve any
// host that is not currently mapped, whether or not the material was cleaned
// up. It exists so a private key for a domain someone else may now control does
// not sit in tls_storage until natural expiry.
type DomainForgetter interface {
	ForgetDomain(ctx context.Context, domain string)
}

// SetCertStatusProvider wires the in-server ACME edge into the custom-domain
// responses. Safe to skip entirely — a nil provider just omits the field.
//
// The same object is picked up as a DomainForgetter when it implements one,
// which avoids a second injection point in the app wiring for what is always
// the same edge.
func (s *Service) SetCertStatusProvider(provider CertStatusProvider) {
	s.certStatus = provider

	if forgetter, ok := provider.(DomainForgetter); ok {
		s.forgetDomain = forgetter
	}
}

// forget drops edge state for a domain that stopped being ours. No-op when
// in-server TLS is off, which is the default.
func (s *Service) forget(ctx context.Context, domain *string) {
	if s.forgetDomain == nil || domain == nil || *domain == "" {
		return
	}

	s.forgetDomain.ForgetDomain(ctx, *domain)
}

// NewService creates a new status pages service. cfg may be nil (e.g. the MCP
// handler doesn't have an app config to hand) — the uptime-bar query's safety
// cap then falls back to the documented retention defaults instead of the
// org's actual configured values. ent may be nil to disable custom-domain quota
// enforcement (callers that never set domains).
func NewService(dbService db.Service, cfg *config.Config, ent *entitlements.Service) *Service {
	return &Service{
		db:            dbService,
		cfg:           cfg,
		ent:           ent,
		verifier:      domainverify.New(),
		verifyLimiter: newVerifyRateLimiter(customDomainVerifyPerMinute, time.Minute),
		unlockLimiter: newUnlockRateLimiter(),
	}
}

// uptimebarHints resolves everything uptimebar needs to bound its queries, ONCE
// per request: the live raw/hour aggregation retention and the org's measured
// probe rate.
//
// Retention is resolved with the same precedence as the aggregation job itself —
// env > performance.* global parameter > legacy koanf field > documented default
// (systemconfig.ResolveReadSideRetention). It must NOT read the koanf config
// alone: the server "Aggregation" settings tab writes the performance.*
// parameters, which never reach the koanf struct, so a stale hint would make
// uptimebar clamp its raw-tier query shorter than the window the job actually
// keeps raw for — silently dropping raw rows no rollup covers yet.
func (s *Service) uptimebarHints(ctx context.Context, orgUID string) uptimebar.Hints {
	rawHours, hourDays := systemconfig.ResolveReadSideRetention(ctx, s.db, s.cfg)

	return uptimebar.Hints{
		RetentionRawHours: rawHours,
		RetentionHourDays: hourDays,
		RawRowsPerHour:    uptimebar.MeasureRawRowsPerHour(ctx, s.db, orgUID),
	}
}

// --- Response types ---

// StatusUpdatePublicResponse represents a status update for public display.
type StatusUpdatePublicResponse struct {
	UID          string    `json:"uid"`
	SectionUID   *string   `json:"sectionUid,omitempty"`
	CheckUID     *string   `json:"checkUid,omitempty"`
	IncidentUID  *string   `json:"incidentUid,omitempty"`
	Title        string    `json:"title"`
	BodyMarkdown string    `json:"bodyMarkdown"`
	LinkURL      *string   `json:"linkUrl,omitempty"`
	Kind         string    `json:"kind"`
	PublishedAt  time.Time `json:"publishedAt"`
}

// PublicIncidentProvider supplies the active incident publications rendered on
// a public status page (spec 2026-08-19-08). It is a small interface set by
// server.go rather than a direct dependency, which keeps the import edge
// one-way: incidentpublications resolves pages through db.Service, and this
// package never has to import it.
type PublicIncidentProvider interface {
	ListPublicIncidents(ctx context.Context, page *models.StatusPage, activeOnly bool) ([]PublicIncident, error)
}

// PublicIncident is the wire shape of one publicly visible incident. It mirrors
// incidentpublications.PublicIncident field for field; it is redeclared here so
// the provider interface can be satisfied without an import cycle, and so this
// package's response type stays self-describing.
type PublicIncident struct {
	UID               string                 `json:"uid"`
	Title             string                 `json:"title"`
	State             string                 `json:"state"`
	Severity          *string                `json:"severity,omitempty"`
	StartedAt         time.Time              `json:"startedAt"`
	ResolvedAt        *time.Time             `json:"resolvedAt,omitempty"`
	AffectedResources []string               `json:"affectedResources,omitempty"`
	Updates           []PublicIncidentUpdate `json:"updates,omitempty"`
}

// PublicIncidentUpdate is one narrative entry on a public incident.
type PublicIncidentUpdate struct {
	UID          string    `json:"uid"`
	Kind         string    `json:"kind"`
	Title        string    `json:"title"`
	BodyMarkdown string    `json:"bodyMarkdown"`
	PublishedAt  time.Time `json:"publishedAt"`
}

// StatusPageResponse represents a status page in API responses.
type StatusPageResponse struct {
	UID              string  `json:"uid"`
	Name             string  `json:"name"`
	Slug             string  `json:"slug"`
	Description      *string `json:"description,omitempty"`
	Visibility       string  `json:"visibility"`
	IsDefault        bool    `json:"isDefault"`
	Enabled          bool    `json:"enabled"`
	ShowAvailability bool    `json:"showAvailability"`
	ShowResponseTime bool    `json:"showResponseTime"`
	HistoryDays      int     `json:"historyDays"`
	HistoryPeriod    string  `json:"historyPeriod"`
	Language         *string `json:"language,omitempty"`
	// AutoPublish / AutoPublishDelaySeconds / AutoResolve are the incident
	// auto-publication settings (spec 2026-08-19-08). They are OPERATOR
	// settings and are echoed on both admin and public payloads, like the other
	// display settings — none of them reveals anything the page does not
	// already show.
	AutoPublish             bool   `json:"autoPublish"`
	AutoPublishDelaySeconds int    `json:"autoPublishDelaySeconds"`
	AutoResolve             string `json:"autoResolve"`
	// HideBranding means different things on the two payload families, and
	// that is deliberate.
	//
	//   * On the ADMIN payloads it is the page's raw opt-in column, so the
	//     dash0 toggle round-trips honestly even while the org is not
	//     entitled. `WhiteLabelAllowed` alongside says whether it has any
	//     effect right now.
	//   * On the PUBLIC payload it is the RESOLVED decision — already ANDed
	//     with the org's `whiteLabel` entitlement — so status0 never has to
	//     know what a plan is, and a downgrade restores the badge with no
	//     client-side logic at all.
	HideBranding bool `json:"hideBranding"`
	// WhiteLabelAllowed reports the org's `whiteLabel` entitlement. Admin
	// payloads only: it is plan information, and a public status page has no
	// business disclosing which plan pays for it.
	WhiteLabelAllowed *bool `json:"whiteLabelAllowed,omitempty"`
	// HasPassword reports that the page is gated by a password. The hash
	// itself is never serialized anywhere.
	HasPassword bool `json:"hasPassword,omitempty"`
	// LogoURL / FaviconURL are the stable public paths of the page's uploaded
	// brand assets (spec 2026-08-21-07), or absent when the slot is unset.
	// They are set on BOTH admin and public payloads: a brand asset is
	// deliberately world-readable, and status0 is the primary consumer.
	LogoURL    *string `json:"logoUrl,omitempty"`
	FaviconURL *string `json:"faviconUrl,omitempty"`
	// CustomCSS is the operator-authored stylesheet the public page injects as
	// a <style> text node. Unlike the custom-domain fields below it is set on
	// the PUBLIC responses too — status0 is its only consumer.
	CustomCSS     *string                      `json:"customCss,omitempty"`
	Sections      []StatusPageSectionResponse  `json:"sections,omitempty"`
	RecentUpdates []StatusUpdatePublicResponse `json:"recentUpdates,omitempty"`
	CreatedAt     *time.Time                   `json:"createdAt,omitempty"`
	// Custom-domain fields are populated ONLY on the authenticated org
	// endpoints (see enrichCustomDomain); the public ViewStatusPage path never
	// sets them, so a custom domain and its challenge token never leak publicly.
	CustomDomain        *string               `json:"customDomain,omitempty"`
	CustomDomainStatus  string                `json:"customDomainStatus,omitempty"`
	CustomDomainRecords []domainverify.Record `json:"customDomainRecords,omitempty"`
	// CustomDomainCertStatus is the in-server-ACME certificate state:
	// "none" (nothing issued yet), "issued", or "error" (last issuance
	// attempt failed — see the server log for the reason). Empty when
	// in-server TLS is disabled or the domain is not verified yet.
	CustomDomainCertStatus string `json:"customDomainCertStatus,omitempty"`
	// CustomDomainState is the lifecycle state (spec 2026-08-23-03):
	// none | pending | active | grace | demoted. `grace` is the one that
	// customDomainStatus cannot express — the page is still being served, but
	// its DNS re-checks are failing and it will go dark if nothing is done.
	CustomDomainState string `json:"customDomainState,omitempty"`
	// CustomDomainDegradedSince is when the domain entered `grace`. Nil
	// otherwise.
	CustomDomainDegradedSince *time.Time `json:"customDomainDegradedSince,omitempty"`
	// CustomDomainLastCheck is the diagnostic from the last re-verification:
	// the mode used, the target expected, what DNS returned. Admin-only, like
	// the domain itself.
	CustomDomainLastCheck *string `json:"customDomainLastCheck,omitempty"`
	// Settings mirrors the status_pages.settings storage shape: nil
	// sub-fields mean "using the default". Present on both admin and public
	// payloads (spec 2026-08-03-01).
	Settings *SettingsResponse `json:"settings,omitempty"`
	// AvailabilityThresholds carries the RESOLVED effective thresholds (never
	// nil/omitted) so public consumers don't need to know the defaults.
	AvailabilityThresholds AvailabilityThresholdsResponse `json:"availabilityThresholds"`
	// OverallStatus is the page-level rollup (spec 2026-08-08-05): one of
	// operational | degraded | down | maintenance | unknown, computed once
	// server-side via models.RollupPageStatus so status0, the public summary
	// endpoint (spec 2026-08-08-06) and the SVG badge (spec 2026-08-08-07)
	// always agree. Only populated on the live public view paths
	// (ViewStatusPage / ViewDefaultStatusPage) — omitted on admin listings
	// that don't load live resource data.
	OverallStatus string `json:"overallStatus,omitempty"`
	// StatusCounts tallies resources per OverallStatus category. Same
	// population rule as OverallStatus.
	StatusCounts *StatusCountsResponse `json:"statusCounts,omitempty"`
	// ActiveIncidents are the currently-open incident publications for this
	// page (spec 2026-08-19-08) — what the banner and the active-incidents
	// section render. Same population rule as OverallStatus: public view paths
	// only. Every field on them is operator-authored or templated from the
	// page's own public resource names; no probe diagnostics can reach here.
	ActiveIncidents []PublicIncident `json:"activeIncidents,omitempty"`
	// OverallAvailabilityPct is the PAGE-level uptime over the page's own
	// history period (spec 2026-08-29-08) — the mean of the per-resource
	// `availability.overallAvailabilityPct` values, resources with no data
	// excluded. Omitted when `showAvailability` is off, and when no resource
	// has any data at all. Public view paths only, like OverallStatus.
	OverallAvailabilityPct *float64 `json:"overallAvailabilityPct,omitempty"`
	// HasKioskToken reports that a kiosk token is minted for this page (spec
	// 2026-08-29-08). ADMIN payloads only: it says something about how the
	// page is reachable, and the token itself is never serialized after the
	// mint call that created it.
	HasKioskToken bool `json:"hasKioskToken,omitempty"`
}

// StatusCountsResponse mirrors models.PageStatusCounts on the wire (spec
// 2026-08-08-05).
type StatusCountsResponse struct {
	Operational int `json:"operational"`
	Degraded    int `json:"degraded"`
	Down        int `json:"down"`
	Maintenance int `json:"maintenance"`
	Unknown     int `json:"unknown"`
}

// StatusPageSummary is the lightweight page-level rollup a status page
// resolves to (spec 2026-08-08-06). It carries just enough for the handler to
// build the public summary response — the URL itself is built by the handler
// since it needs the incoming request's scheme/host.
type StatusPageSummary struct {
	Status   models.PageStatus
	Counts   models.PageStatusCounts
	PageName string
	PageSlug string
	// CustomDomain is the verified, active custom domain for this page, or ""
	// when the page is served path-based only.
	CustomDomain string
	// Visibility is the page's stored visibility (`public`, `password` or
	// `private`). It never reaches the wire — it exists so the handler can
	// decide who may cache the response, which depends on the page rather than
	// on whether this particular caller was let in (spec 2026-08-22-06).
	Visibility string
	// OverallAvailabilityPct is the page-level uptime over the page's history
	// period (spec 2026-08-29-08), or nil when the page hides availability,
	// nothing has data, or the caller asked for the cheap variant (the SVG
	// badge, which has nowhere to render a percentage).
	OverallAvailabilityPct *float64
}

// AvailabilitySettingsResponse mirrors models.AvailabilitySettings on the
// wire — nil fields mean "using the default".
type AvailabilitySettingsResponse struct {
	ThresholdUp       *float64 `json:"thresholdUp,omitempty"`
	ThresholdDegraded *float64 `json:"thresholdDegraded,omitempty"`
}

// SettingsResponse mirrors models.StatusPageSettings on the wire.
type SettingsResponse struct {
	Availability *AvailabilitySettingsResponse `json:"availability,omitempty"`
}

// AvailabilityThresholdsResponse carries the resolved (never-nil) effective
// availability thresholds for a page, e.g. for a future legend/target line.
type AvailabilityThresholdsResponse struct {
	ThresholdUp       float64 `json:"thresholdUp"`
	ThresholdDegraded float64 `json:"thresholdDegraded"`
}

// StatusPageSectionResponse represents a section in API responses.
type StatusPageSectionResponse struct {
	UID       string                       `json:"uid"`
	Name      string                       `json:"name"`
	Slug      string                       `json:"slug"`
	Position  int                          `json:"position"`
	Resources []StatusPageResourceResponse `json:"resources,omitempty"`
	CreatedAt *time.Time                   `json:"createdAt,omitempty"`
}

// StatusPageResourceResponse represents a resource in API responses. Exactly
// one of CheckUID / CheckGroupUID is set (spec 2026-08-01-03).
type StatusPageResourceResponse struct {
	UID string `json:"uid"`
	// CheckUID is set when the resource targets an individual check.
	CheckUID *string `json:"checkUid,omitempty"`
	// CheckGroupUID is set when the resource targets a check group, rendered as
	// one aggregated component. It is an opaque UUID and reveals no topology,
	// so it is carried on the public payload too — unlike the group's members,
	// which are never exposed.
	CheckGroupUID *string `json:"checkGroupUid,omitempty"`
	PublicName    *string `json:"publicName,omitempty"`
	Explanation   *string `json:"explanation,omitempty"`
	// AutoPublish is the per-resource auto-publication override (spec
	// 2026-08-19-08). Three-state: absent/null means "inherit the page", which
	// is NOT the same as an explicit false.
	AutoPublish  *bool                     `json:"autoPublish,omitempty"`
	Position     int                       `json:"position"`
	Check        *ResourceCheckInfo        `json:"check,omitempty"`
	Availability *ResourceAvailabilityData `json:"availability,omitempty"`
	CreatedAt    *time.Time                `json:"createdAt,omitempty"`
}

// ResourceCheckInfo contains live data for a resource. For a group resource it
// carries the GROUP's name and its rolled-up status; Type is deliberately left
// empty (a "group" type string would itself hint that the component aggregates
// several probes), and no member name, type or count ever appears here.
type ResourceCheckInfo struct {
	Name *string `json:"name,omitempty"`
	// Type is the check type ("http", "tcp", …) for a check resource, and empty
	// for a group resource — strict topology hiding (spec 2026-08-01-03).
	Type   string `json:"type"`
	Status string `json:"status"`
	// InMaintenance is true when the check — or, for a group resource, the group
	// or any of its members — is inside an active maintenance window at request
	// time, so the public page can show a "Scheduled Maintenance" badge instead
	// of a raw up/down state.
	InMaintenance bool `json:"inMaintenance"`
}

// ResourceAvailabilityData contains availability and performance data for public display.
type ResourceAvailabilityData struct {
	OverallAvailabilityPct *float64 `json:"overallAvailabilityPct,omitempty"`
	// DailyAvailability holds the per-bucket availability points. The JSON key is
	// kept for back-compat even when BucketUnit is "hour" (hourly buckets); read
	// BucketUnit/Period to label the axis correctly.
	DailyAvailability []AvailabilityPoint `json:"dailyAvailability,omitempty"`
	// ResponseTimeSeries holds one series per region the check has reported
	// results from (spec 2026-08-14-04). Replaces the old flat
	// `responseTimeData: []ResponseTimePoint` field, which interleaved every
	// region's points into one meaningless saw-tooth series — status0 (the
	// SPA and the embed widget) was the only consumer of that field and this
	// substructure was never documented in openapi.yaml, so it is dropped
	// rather than kept alongside.
	ResponseTimeSeries []ResponseTimeSeries `json:"responseTimeSeries,omitempty"`
	// Period is the active history period ("24h"|"7d"|"30d"|"90d").
	Period string `json:"period,omitempty"`
	// BucketUnit is the granularity of each point: "day" or "hour".
	BucketUnit string `json:"bucketUnit,omitempty"`
}

// AvailabilityPoint represents availability data for a single bucket (a day in
// daily mode, an hour in 24h mode). Date is the UTC calendar date (kept for
// back-compat); Time is the bucket start as RFC3339 and is the authoritative
// anchor for hourly buckets.
type AvailabilityPoint struct {
	Date            string  `json:"date"`
	Time            string  `json:"time,omitempty"`
	AvailabilityPct float64 `json:"availabilityPct"`
	Status          string  `json:"status"`
}

// ResponseTimePoint represents response time data for a single time period (hourly granularity).
type ResponseTimePoint struct {
	Time        string   `json:"time"`
	DurationP95 *float32 `json:"durationP95,omitempty"`
	// Status indicates the check's outcome at this point: "up", "down",
	// "timeout", "error", or "" when not applicable.
	Status string `json:"status,omitempty"`
	// AvailabilityPct is the probe-ratio availability of the slice this point
	// covers: 100 or 0 for a single raw probe, the rollup's
	// successful/total × 100 for an aggregated one. Null when the row carries
	// no countable probe (a lifecycle marker, an abandoned attempt) — no data
	// is not 100%.
	//
	// It exists so the strip under the public response-time chart can be
	// colored by AVAILABILITY while staying aligned to the chart's own slots:
	// the point IS the slot, so there is nothing left to align (spec
	// 2026-08-26-10 phase 2).
	AvailabilityPct *float64 `json:"availabilityPct,omitempty"`
	// TotalChecks / SuccessfulChecks are the counts behind AvailabilityPct,
	// carried so the front end can render "59 / 60 probes up" without doing any
	// availability math of its own.
	TotalChecks      int `json:"totalChecks,omitempty"`
	SuccessfulChecks int `json:"successfulChecks,omitempty"`
	// AvailabilityStatus is the shared up/degraded/down/noData vocabulary
	// (uptimebar.Classify) applied to AvailabilityPct with the PAGE's effective
	// thresholds — the same classifier and the same thresholds as the
	// availability bar above the chart, so the two can never disagree about the
	// same numbers. Distinct from Status, which is the probe's own outcome.
	AvailabilityStatus string `json:"availabilityStatus,omitempty"`
}

// ResponseTimeSeries is one region's response-time points for a resource.
// Region is nil for results with no recorded region (NULL — legacy rows from
// before regions were tracked, or a check with a single implicit region).
type ResponseTimeSeries struct {
	Region *string             `json:"region,omitempty"`
	Points []ResponseTimePoint `json:"points"`
}

// --- Request types ---

// AvailabilitySettingsInput is the wire shape for a page's availability
// threshold section on write, decoded with unknown keys rejected
// (DisallowUnknownFields, applied by the handler — see parseSettingsField).
type AvailabilitySettingsInput struct {
	ThresholdUp       *float64 `json:"thresholdUp,omitempty"`
	ThresholdDegraded *float64 `json:"thresholdDegraded,omitempty"`
}

// SettingsInput mirrors status_pages.settings on write. Availability is nil
// when the "availability" key is either absent or an explicit JSON null;
// AvailabilitySet (populated by the handler via raw-body presence detection,
// not by the JSON decode itself) distinguishes "absent" (leave untouched)
// from "present" (object ⇒ replace the section wholly, null ⇒ reset it to
// defaults) per the PATCH no-deep-merge semantics (spec 2026-08-03-01).
type SettingsInput struct {
	Availability    *AvailabilitySettingsInput `json:"availability,omitempty"`
	AvailabilitySet bool                       `json:"-"`
}

// CreateStatusPageRequest represents a request to create a status page.
type CreateStatusPageRequest struct {
	Name             string  `json:"name"`
	Slug             string  `json:"slug"`
	Description      *string `json:"description,omitempty"`
	Visibility       *string `json:"visibility,omitempty"`
	IsDefault        *bool   `json:"isDefault,omitempty"`
	ShowAvailability *bool   `json:"showAvailability,omitempty"`
	ShowResponseTime *bool   `json:"showResponseTime,omitempty"`
	HistoryDays      *int    `json:"historyDays,omitempty"`
	HistoryPeriod    *string `json:"historyPeriod,omitempty"`
	Language         *string `json:"language,omitempty"`
	// AutoPublish / AutoPublishDelaySeconds / AutoResolve configure the
	// incident auto-publication pipeline (spec 2026-08-19-08). Omitting
	// autoPublish on a NEW page leaves it at the create-path default, which is
	// ON — existing pages stay off, new ones opt in.
	AutoPublish             *bool   `json:"autoPublish,omitempty"`
	AutoPublishDelaySeconds *int    `json:"autoPublishDelaySeconds,omitempty"`
	AutoResolve             *string `json:"autoResolve,omitempty"`
	// HideBranding opts the new page out of the "powered by SolidPing" footer.
	// It only takes effect while the org holds the `whiteLabel` entitlement —
	// the flag is stored either way, so an upgrade does not need the operator
	// to come back and re-tick it.
	HideBranding *bool `json:"hideBranding,omitempty"`
	// Password sets the unlock password for a `visibility: password` page.
	// WRITE-ONLY: it is hashed on the way in (internal/utils/passwords —
	// argon2id by default) and never appears on
	// any response.
	Password *string `json:"password,omitempty"`
	// CustomCSS optionally sets the page's custom stylesheet at create time.
	// Max 64 KB, no @import (see validateCustomCSS).
	CustomCSS *string `json:"customCss,omitempty"`
	// CustomDomain optionally sets a custom domain on the new page (a non-empty
	// value generates a token; empty/nil means no domain). Verification still
	// happens afterward via the verify endpoint.
	CustomDomain *string `json:"customDomain,omitempty"`
	// Settings optionally sets the page's settings (e.g. availability
	// thresholds) at create time. Populated by the handler from a strict,
	// unknown-key-rejecting decode — NOT by the default request decode (hence
	// json:"-").
	Settings *SettingsInput `json:"-"`
	// CheckUIDs optionally seeds the page's default "Services" section with
	// one resource per check, in request order (spec 2026-08-28-16). Every
	// entry must resolve to a check in this org, or the WHOLE request is
	// rejected — nothing is written, including the page itself.
	CheckUIDs []string `json:"checkUids,omitempty"`
}

// UpdateStatusPageRequest represents a request to update a status page.
type UpdateStatusPageRequest struct {
	Name             *string `json:"name,omitempty"`
	Slug             *string `json:"slug,omitempty"`
	Description      *string `json:"description,omitempty"`
	Visibility       *string `json:"visibility,omitempty"`
	IsDefault        *bool   `json:"isDefault,omitempty"`
	Enabled          *bool   `json:"enabled,omitempty"`
	ShowAvailability *bool   `json:"showAvailability,omitempty"`
	ShowResponseTime *bool   `json:"showResponseTime,omitempty"`
	HistoryDays      *int    `json:"historyDays,omitempty"`
	HistoryPeriod    *string `json:"historyPeriod,omitempty"`
	Language         *string `json:"language,omitempty"`
	// AutoPublish / AutoPublishDelaySeconds / AutoResolve configure the
	// incident auto-publication pipeline (spec 2026-08-19-08).
	AutoPublish             *bool   `json:"autoPublish,omitempty"`
	AutoPublishDelaySeconds *int    `json:"autoPublishDelaySeconds,omitempty"`
	AutoResolve             *string `json:"autoResolve,omitempty"`
	// HideBranding flips the page's white-label opt-in. nil leaves it.
	HideBranding *bool `json:"hideBranding,omitempty"`
	// Password sets, replaces or clears the unlock password: a non-empty
	// string re-hashes it (which also invalidates every outstanding unlock
	// cookie, since the cookie's HMAC key is derived from the hash), an empty
	// string clears it, and an omitted field leaves it untouched.
	// WRITE-ONLY — never echoed back.
	Password *string `json:"password,omitempty"`
	// CustomCSS sets, replaces or clears the page's custom stylesheet: an empty
	// string clears the column, an omitted field leaves it untouched. Max
	// 64 KB, no @import (see validateCustomCSS).
	CustomCSS *string `json:"customCss,omitempty"`
	// CustomDomain sets/changes/clears the page's custom domain. A non-empty
	// value sets it (generating a fresh token); null or "" clears it. Whether
	// the key was present at all is carried in CustomDomainSet (presence
	// detection) so an omitted field leaves the domain untouched.
	CustomDomain *string `json:"customDomain,omitempty"`
	// CustomDomainSet is populated by the handler from the raw body, NOT from
	// JSON — it distinguishes "customDomain omitted" from an explicit null/"".
	CustomDomainSet bool `json:"-"`
	// Settings updates the page's settings (e.g. availability thresholds).
	// Populated by the handler from a strict, unknown-key-rejecting decode of
	// the raw body — NOT by the default request decode (hence json:"-").
	// SettingsPresent distinguishes "settings key absent" (leave untouched)
	// from "present" (Settings nil ⇒ explicit top-level null; non-nil ⇒
	// object, see SettingsInput.AvailabilitySet for section-level presence).
	Settings        *SettingsInput `json:"-"`
	SettingsPresent bool           `json:"-"`
}

// CreateSectionRequest represents a request to create a section.
type CreateSectionRequest struct {
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Position *int   `json:"position,omitempty"`
}

// UpdateSectionRequest represents a request to update a section.
type UpdateSectionRequest struct {
	Name     *string `json:"name,omitempty"`
	Slug     *string `json:"slug,omitempty"`
	Position *int    `json:"position,omitempty"`
}

// CreateResourceRequest represents a request to add a check OR a check group to
// a section. Exactly one of CheckUID / CheckGroupUID must be set (spec
// 2026-08-01-03).
type CreateResourceRequest struct {
	// CheckUID is a check UID or slug. Mutually exclusive with CheckGroupUID.
	CheckUID string `json:"checkUid"`
	// CheckGroupUID is a check group UID or slug. Mutually exclusive with
	// CheckUID. A group resource renders as one aggregated public component.
	CheckGroupUID string  `json:"checkGroupUid"`
	PublicName    *string `json:"publicName,omitempty"`
	Explanation   *string `json:"explanation,omitempty"`
	// AutoPublish overrides the page's auto-publish setting for this resource.
	// Omitted = inherit.
	AutoPublish *bool `json:"autoPublish,omitempty"`
	Position    *int  `json:"position,omitempty"`
}

// UpdateResourceRequest represents a request to update a resource. Supplying
// CheckUID or CheckGroupUID switches the resource's target kind; supplying both
// is a validation error, supplying neither leaves the target untouched.
type UpdateResourceRequest struct {
	CheckUID      *string `json:"checkUid,omitempty"`
	CheckGroupUID *string `json:"checkGroupUid,omitempty"`
	PublicName    *string `json:"publicName,omitempty"`
	Explanation   *string `json:"explanation,omitempty"`
	// AutoPublish sets or clears the per-resource override. AutoPublishSet is
	// populated by the handler from the raw body so an omitted key ("leave
	// alone") stays distinguishable from an explicit null ("reset to inherit").
	AutoPublish    *bool `json:"autoPublish,omitempty"`
	AutoPublishSet bool  `json:"-"`
	Position       *int  `json:"position,omitempty"`
}

// --- Options ---

// GetStatusPageOptions contains optional parameters for getting a status page.
type GetStatusPageOptions struct {
	IncludeSections bool
}

// --- StatusPage CRUD ---

// ListStatusPages retrieves all status pages for an organization.
func (s *Service) ListStatusPages(ctx context.Context, orgSlug string) ([]StatusPageResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	pages, err := s.db.ListStatusPages(ctx, org.UID)
	if err != nil {
		return nil, err
	}

	// One entitlement resolve for the whole list, not one per page: the flag is
	// org-wide, and a list of 50 pages must not become 50 lookups.
	allowed := s.whiteLabelAllowed(ctx, org.UID)

	responses := make([]StatusPageResponse, len(pages))
	for i, page := range pages {
		responses[i] = convertPageToResponse(page)
		s.enrichCustomDomain(&responses[i], page)
		s.enrichAdminBranding(&responses[i], allowed)
	}

	return responses, nil
}

// applyCreateFields sets optional fields from the create request onto the page model.
func applyCreateFields(page *models.StatusPage, req *CreateStatusPageRequest) {
	if req.Description != nil {
		page.Description = req.Description
	}

	if req.Visibility != nil {
		page.Visibility = *req.Visibility
	}

	if req.ShowAvailability != nil {
		page.ShowAvailability = *req.ShowAvailability
	}

	if req.ShowResponseTime != nil {
		page.ShowResponseTime = *req.ShowResponseTime
	}

	// History window: the period enum is the source of truth. Accept the legacy
	// historyDays too (mapped to the enum) for one release. When the enum is set,
	// keep history_days roughly in sync for back-compat (24h → 1).
	if req.HistoryPeriod != nil {
		page.HistoryPeriod = *req.HistoryPeriod
		page.HistoryDays = daysForPeriod(models.StatusPagePeriod(*req.HistoryPeriod))
	} else if req.HistoryDays != nil {
		page.HistoryDays = *req.HistoryDays
		page.HistoryPeriod = string(models.PeriodFromDays(*req.HistoryDays))
	}

	if req.Language != nil {
		page.Language = req.Language
	}

	if req.HideBranding != nil {
		// The white-label opt-in lives in settings -> branding, alongside the
		// two brand-asset UIDs (spec 2026-08-22-03). A brand-new page has no
		// branding section yet, so it is created here rather than merged.
		page.Settings.Branding = &models.BrandingSettings{HideBranding: *req.HideBranding}
	}

	if req.AutoPublish != nil {
		page.AutoPublish = *req.AutoPublish
	}

	if req.AutoPublishDelaySeconds != nil {
		page.AutoPublishDelaySeconds = *req.AutoPublishDelaySeconds
	}

	if req.AutoResolve != nil {
		page.AutoResolve = *req.AutoResolve
	}

	// An empty stylesheet is "no stylesheet": leave the column NULL rather than
	// storing '', matching the update path's clear semantics.
	if req.CustomCSS != nil && *req.CustomCSS != "" {
		page.CustomCSS = req.CustomCSS
	}

	// No PATCH merge semantics at create time (nothing to merge against): a
	// given "availability" object sets the section directly; an explicit null
	// or an omitted key both leave it at the zero value (defaults).
	if avail := createRequestAvailability(req); avail != nil {
		page.Settings.Availability = avail
	}
}

// createRequestAvailability copies the create request's availability section
// (if a non-null object was given) into a fresh models.AvailabilitySettings,
// or nil when absent/explicit-null (both mean "use the defaults" at create
// time, since there's no prior value to preserve).
func createRequestAvailability(req *CreateStatusPageRequest) *models.AvailabilitySettings {
	if req.Settings == nil || req.Settings.Availability == nil {
		return nil
	}

	return &models.AvailabilitySettings{
		ThresholdUp:       req.Settings.Availability.ThresholdUp,
		ThresholdDegraded: req.Settings.Availability.ThresholdDegraded,
	}
}

// daysForPeriod returns a back-compat history_days count for a period enum.
// 24h has no day equivalent, so it maps to 1 (the smallest sane value) purely
// to keep the deprecated column populated; bucketing always uses the enum.
func daysForPeriod(period models.StatusPagePeriod) int {
	switch period {
	case models.StatusPagePeriod24h:
		return 1
	case models.StatusPagePeriod7d:
		return 7
	case models.StatusPagePeriod30d:
		return 30
	case models.StatusPagePeriod90d:
		return 90
	default:
		return 90
	}
}

// validateCreateStatusPage runs every pre-write validation for a create
// request and returns the password hash to store (nil = none). Bundled into
// one function so CreateStatusPage itself stays under the
// cyclomatic-complexity budget, mirroring validateStatusPageUpdate.
func validateCreateStatusPage(req *CreateStatusPageRequest) (*string, error) {
	if err := validateHistoryPeriod(req.HistoryPeriod); err != nil {
		return nil, err
	}

	if err := validateAutoPublishSettings(req.AutoResolve, req.AutoPublishDelaySeconds); err != nil {
		return nil, err
	}

	if err := validateCustomCSS(req.CustomCSS); err != nil {
		return nil, err
	}

	if err := validateVisibility(req.Visibility); err != nil {
		return nil, err
	}

	if err := validateAvailabilitySettings(createRequestAvailability(req)); err != nil {
		return nil, err
	}

	// nil `current` — there is no page yet, so "password visibility needs a
	// password" has no prior hash to fall back on.
	return resolvePasswordChange(nil, req.Visibility, req.Password)
}

// resolveCreateCheckUIDs resolves every entry of a create-status-page
// request's checkUids to a canonical check UID within the org, preserving
// request order. It is called BEFORE any write so a bad entry rejects the
// whole request with nothing persisted — see CreateStatusPage.
func (s *Service) resolveCreateCheckUIDs(
	ctx context.Context, orgUID string, requested []string,
) ([]string, error) {
	if len(requested) == 0 {
		return nil, nil
	}

	resolved := make([]string, len(requested))

	for i, uid := range requested {
		check, err := s.db.GetCheckByUidOrSlug(ctx, orgUID, uid)
		if err != nil || check == nil {
			return nil, fmt.Errorf("%w: %s", ErrCheckUIDInvalid, uid)
		}

		resolved[i] = check.UID
	}

	return resolved, nil
}

// CreateStatusPage creates a new status page.
func (s *Service) CreateStatusPage(
	ctx context.Context, orgSlug string, req *CreateStatusPageRequest,
) (StatusPageResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return StatusPageResponse{}, ErrOrganizationNotFound
	}

	passwordHash, errValidate := validateCreateStatusPage(req)
	if errValidate != nil {
		return StatusPageResponse{}, errValidate
	}

	taken := func(ctx context.Context, slug string) (bool, error) {
		existing, errGet := s.db.GetStatusPageBySlug(ctx, org.UID, slug)
		if errGet != nil && !errors.Is(errGet, sql.ErrNoRows) {
			return false, errGet
		}

		return existing != nil, nil
	}

	slug, err := s.resolveCreateSlug(ctx, req.Slug, req.Name, slugFallbackPage, taken)
	if err != nil {
		return StatusPageResponse{}, err
	}

	// Resolve every requested check BEFORE writing anything: an unknown or
	// foreign-org UID must reject the whole request with nothing persisted,
	// not even the page (spec 2026-08-28-16).
	checkUIDs, err := s.resolveCreateCheckUIDs(ctx, org.UID, req.CheckUIDs)
	if err != nil {
		return StatusPageResponse{}, err
	}

	page := models.NewStatusPage(org.UID, req.Name, slug)
	applyCreateFields(page, req)
	page.PasswordHash = passwordHash

	// Check if this should be default (first page or explicitly set)
	existingPages, _ := s.db.ListStatusPages(ctx, org.UID)
	if len(existingPages) == 0 {
		page.IsDefault = true
	} else if req.IsDefault != nil && *req.IsDefault {
		if errClear := s.clearDefaultStatusPage(ctx, org.UID); errClear != nil {
			return StatusPageResponse{}, errClear
		}
		page.IsDefault = true
	}

	// Every new page gets a default "Services" section (spec 2026-08-28-16)
	// — nobody wants zero sections, and it stays renamable/deletable like any
	// other. Page + section + any initial resources land in ONE transaction:
	// no half-created page if a resource insert fails.
	section := models.NewStatusPageSection(page.UID, defaultSectionName, defaultSectionSlug, 0)

	resources := make([]*models.StatusPageResource, len(checkUIDs))
	for i, checkUID := range checkUIDs {
		resources[i] = models.NewStatusPageResource(section.UID, checkUID, i)
	}

	if errCreate := s.db.CreateStatusPageWithDefaultSection(ctx, page, section, resources); errCreate != nil {
		return StatusPageResponse{}, errCreate
	}

	// Product analytics (spec 2026-08-02-08). Captured at the SERVICE layer,
	// not in the REST handler: the MCP create-status-page tool
	// (internal/mcp/tools_statuspages.go) calls this method directly, so a
	// handler-level capture would silently miss every MCP-created page. Same
	// reasoning as the update path's capturePublishTransition.
	s.capturePublishTransition(ctx, org.UID, nil, page)

	audit.Record(ctx, s.db, org.UID, models.EventTypeStatusPageCreated,
		auditTarget(page), models.JSONMap{
			fieldSlug:       page.Slug,
			fieldVisibility: page.Visibility,
			"enabled":       page.Enabled,
		})

	page, err = s.applyCreateCustomDomain(ctx, org.UID, page, req)
	if err != nil {
		return StatusPageResponse{}, err
	}

	response := convertPageToResponse(page)
	s.enrichCustomDomain(&response, page)
	s.enrichAdminBranding(&response, s.whiteLabelAllowed(ctx, org.UID))

	return response, nil
}

// applyCreateCustomDomain binds a custom domain supplied at create time. It runs
// as a second step because it needs the persisted UID for the conflict check
// and token write, and reloads the page so the response reflects it.
func (s *Service) applyCreateCustomDomain(
	ctx context.Context, orgUID string, page *models.StatusPage, req *CreateStatusPageRequest,
) (*models.StatusPage, error) {
	if req.CustomDomain == nil || strings.TrimSpace(*req.CustomDomain) == "" {
		return page, nil
	}

	if err := s.setCustomDomain(ctx, orgUID, page, strings.TrimSpace(*req.CustomDomain)); err != nil {
		return nil, err
	}

	return s.db.GetStatusPage(ctx, orgUID, page.UID)
}

// GetStatusPage retrieves a single status page by UID or slug.
func (s *Service) GetStatusPage(
	ctx context.Context, orgSlug, identifier string, opts GetStatusPageOptions,
) (StatusPageResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return StatusPageResponse{}, ErrOrganizationNotFound
	}

	page, err := s.db.GetStatusPageByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || page == nil {
		return StatusPageResponse{}, ErrStatusPageNotFound
	}

	response := convertPageToResponse(page)
	s.enrichCustomDomain(&response, page)
	s.enrichAdminBranding(&response, s.whiteLabelAllowed(ctx, org.UID))

	if opts.IncludeSections {
		sections, err := s.loadSectionsWithResources(ctx, page.UID)
		if err != nil {
			return StatusPageResponse{}, err
		}

		// Enrich resources with live check / group data. This admin path does
		// not surface OverallStatus/StatusCounts — those are populated only on
		// the public view paths (ViewStatusPage / ViewDefaultStatusPage).
		s.enrichResourceInfo(ctx, org.UID, sections)

		response.Sections = sections
	}

	return response, nil
}

// validateStatusPageUpdate runs every pre-write validation for an update
// request (slug conflict, history period enum, custom CSS, settings PATCH
// semantics + effective-threshold check) and returns the resolved settings
// value to persist (nil = leave the column untouched). Bundled into one
// function so UpdateStatusPage itself stays under the cyclomatic-complexity
// budget.
func (s *Service) validateStatusPageUpdate(
	ctx context.Context, orgUID string, page *models.StatusPage, req *UpdateStatusPageRequest,
) (*models.StatusPageSettings, error) {
	if err := s.validatePageSlugChange(ctx, orgUID, page.Slug, req.Slug); err != nil {
		return nil, err
	}

	if err := validateHistoryPeriod(req.HistoryPeriod); err != nil {
		return nil, err
	}

	if err := validateAutoPublishSettings(req.AutoResolve, req.AutoPublishDelaySeconds); err != nil {
		return nil, err
	}

	if err := validateCustomCSS(req.CustomCSS); err != nil {
		return nil, err
	}

	if err := validateVisibility(req.Visibility); err != nil {
		return nil, err
	}

	return resolveSettingsUpdate(page.Settings, req)
}

// UpdateStatusPage updates an existing status page.
func (s *Service) UpdateStatusPage(
	ctx context.Context, orgSlug, identifier string, req *UpdateStatusPageRequest,
) (StatusPageResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return StatusPageResponse{}, ErrOrganizationNotFound
	}

	page, err := s.db.GetStatusPageByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || page == nil {
		return StatusPageResponse{}, ErrStatusPageNotFound
	}

	newSettings, errVal := s.validateStatusPageUpdate(ctx, org.UID, page, req)
	if errVal != nil {
		return StatusPageResponse{}, errVal
	}

	passwordHash, errPwd := resolvePasswordChange(page, req.Visibility, req.Password)
	if errPwd != nil {
		return StatusPageResponse{}, errPwd
	}

	// Handle default toggle
	if req.IsDefault != nil && *req.IsDefault && !page.IsDefault {
		if errClear := s.clearDefaultStatusPage(ctx, org.UID); errClear != nil {
			return StatusPageResponse{}, errClear
		}
	}

	update := models.StatusPageUpdate{
		Name:             req.Name,
		Slug:             req.Slug,
		Description:      req.Description,
		Visibility:       req.Visibility,
		IsDefault:        req.IsDefault,
		Enabled:          req.Enabled,
		ShowAvailability: req.ShowAvailability,
		ShowResponseTime: req.ShowResponseTime,
		HistoryDays:      req.HistoryDays,
		HistoryPeriod:    req.HistoryPeriod,
		Language:         req.Language,
		HideBranding:     req.HideBranding,
		PasswordHash:     passwordHash,
		CustomCSS:        req.CustomCSS,
		Settings:         newSettings,

		AutoPublish:             req.AutoPublish,
		AutoPublishDelaySeconds: req.AutoPublishDelaySeconds,
		AutoResolve:             req.AutoResolve,
	}

	// The period enum is the source of truth; keep history_days in sync for
	// back-compat when only the enum is sent. When the legacy historyDays is
	// sent without an enum, derive the enum so the column stays consistent.
	if req.HistoryPeriod != nil {
		days := daysForPeriod(models.StatusPagePeriod(*req.HistoryPeriod))
		update.HistoryDays = &days
	} else if req.HistoryDays != nil {
		derived := string(models.PeriodFromDays(*req.HistoryDays))
		update.HistoryPeriod = &derived
	}

	if errUpdate := s.db.UpdateStatusPage(ctx, page.UID, &update); errUpdate != nil {
		return StatusPageResponse{}, errUpdate
	}

	updated, err := s.db.GetStatusPage(ctx, org.UID, page.UID)
	if err != nil {
		return StatusPageResponse{}, err
	}

	s.capturePublishTransition(ctx, org.UID, page, updated)

	if changed, safe := audit.Changes(auditSnapshot(page), auditSnapshot(updated)); len(changed) > 0 {
		audit.Record(ctx, s.db, org.UID, models.EventTypeStatusPageUpdated,
			auditTarget(updated), audit.ChangePayload(changed, safe, nil, nil))
	}

	return s.finalizeCustomDomainUpdate(ctx, org.UID, updated, req)
}

// auditTarget names a status page for the audit trail.
func auditTarget(page *models.StatusPage) audit.Target {
	return audit.Target{Type: "status_page", UID: page.UID, Name: page.Name}
}

// auditSnapshot is the scalar shape the audit trail diffs an update against.
//
// password_hash is present ON PURPOSE and is exactly why audit.Changes
// separates "which fields moved" from "what they moved to": IsSensitiveKey
// matches it, so the rotation is reported as a changed field name and the hash
// itself never leaves this function.
func auditSnapshot(page *models.StatusPage) map[string]any {
	description := ""
	if page.Description != nil {
		description = *page.Description
	}

	customDomain := ""
	if page.CustomDomain != nil {
		customDomain = *page.CustomDomain
	}

	passwordHash := ""
	if page.PasswordHash != nil {
		passwordHash = *page.PasswordHash
	}

	return map[string]any{
		"name":               page.Name,
		fieldSlug:            page.Slug,
		"description":        description,
		fieldVisibility:      page.Visibility,
		"enabled":            page.Enabled,
		"is_default":         page.IsDefault,
		"show_availability":  page.ShowAvailability,
		"show_response_time": page.ShowResponseTime,
		"history_period":     page.HistoryPeriod,
		"custom_domain":      customDomain,
		"auto_publish":       page.AutoPublish,
		"auto_resolve":       page.AutoResolve,
		"password_hash":      passwordHash,
	}
}

// isPublished reports whether a status page is live and world-readable, which
// is what "published" means for the status_page_published product event.
func isPublished(page *models.StatusPage) bool {
	return page != nil && page.Enabled && page.Visibility == visibilityPublic
}

// finalizeCustomDomainUpdate applies the custom-domain set/clear (a separate
// write with its own normalization, quota, conflict, and token semantics),
// reloads the page if the domain changed, and builds the enriched response.
func (s *Service) finalizeCustomDomainUpdate(
	ctx context.Context, orgUID string, page *models.StatusPage, req *UpdateStatusPageRequest,
) (StatusPageResponse, error) {
	if err := s.applyCustomDomainChange(ctx, orgUID, page, req); err != nil {
		return StatusPageResponse{}, err
	}

	if req.CustomDomainSet {
		reloaded, err := s.db.GetStatusPage(ctx, orgUID, page.UID)
		if err != nil {
			return StatusPageResponse{}, err
		}

		page = reloaded
	}

	response := convertPageToResponse(page)
	s.enrichCustomDomain(&response, page)
	s.enrichAdminBranding(&response, s.whiteLabelAllowed(ctx, orgUID))

	return response, nil
}

// DeleteStatusPage soft-deletes a status page.
func (s *Service) DeleteStatusPage(ctx context.Context, orgSlug, identifier string) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return ErrOrganizationNotFound
	}

	page, err := s.db.GetStatusPageByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || page == nil {
		return ErrStatusPageNotFound
	}

	if delErr := s.db.DeleteStatusPage(ctx, page.UID); delErr != nil {
		return delErr
	}

	audit.Record(ctx, s.db, org.UID, models.EventTypeStatusPageDeleted,
		auditTarget(page), models.JSONMap{fieldSlug: page.Slug})

	// Reap the page's brand assets. This is NOT housekeeping: a status-page
	// logo is public exactly while its file row is live, so without this the
	// deleted page's logo stays readable on /pub/assets/:uid forever. Before
	// spec 2026-08-22-03 the page row disappearing handled it implicitly,
	// because the access check went through the page; now nothing does.
	//
	// db.DeleteFilesByTopicPrefix is the same primitive files.DeleteAttachments
	// wraps — used directly because this service holds no files.Service and
	// injecting one to reach the identical query would be ceremony.
	if _, reapErr := s.db.DeleteFilesByTopicPrefix(
		ctx, org.UID, files.StatusPageTopicPrefix(page.UID),
	); reapErr != nil {
		// Best-effort: the page is already gone. Surfacing the error would turn
		// a successful delete into a 500 the caller cannot act on.
		slog.WarnContext(ctx, "failed to reap status page attachments",
			"error", reapErr, "orgUID", org.UID, "statusPageUID", page.UID)
	}

	return nil
}

// --- Section CRUD ---

// ListSections retrieves all sections for a status page.
func (s *Service) ListSections(
	ctx context.Context, orgSlug, pageIdentifier string,
) ([]StatusPageSectionResponse, error) {
	page, err := s.resolveStatusPage(ctx, orgSlug, pageIdentifier)
	if err != nil {
		return nil, err
	}

	sections, err := s.db.ListStatusPageSections(ctx, page.UID)
	if err != nil {
		return nil, err
	}

	responses := make([]StatusPageSectionResponse, len(sections))
	for i, section := range sections {
		responses[i] = convertSectionToResponse(section)
	}

	return responses, nil
}

// resolveSectionPosition picks the position for a new section: the caller's
// requested value if any, otherwise max(existing) + 1 so the section appends
// at the end. Defaulting to 0 would force every new section to share
// position=0, breaking the swap-based reorder UI.
func (s *Service) resolveSectionPosition(ctx context.Context, pageUID string, requested *int) (int, error) {
	if requested != nil {
		return *requested, nil
	}

	maxPosition, err := s.db.MaxStatusPageSectionPosition(ctx, pageUID)
	if err != nil {
		return 0, fmt.Errorf("failed to compute next section position: %w", err)
	}

	return maxPosition + 1, nil
}

// resolveResourcePosition is the per-section equivalent of resolveSectionPosition.
func (s *Service) resolveResourcePosition(ctx context.Context, sectionUID string, requested *int) (int, error) {
	if requested != nil {
		return *requested, nil
	}

	maxPosition, err := s.db.MaxStatusPageResourcePosition(ctx, sectionUID)
	if err != nil {
		return 0, fmt.Errorf("failed to compute next resource position: %w", err)
	}

	return maxPosition + 1, nil
}

// CreateSection creates a new section within a status page.
func (s *Service) CreateSection(
	ctx context.Context, orgSlug, pageIdentifier string, req CreateSectionRequest,
) (StatusPageSectionResponse, error) {
	page, err := s.resolveStatusPage(ctx, orgSlug, pageIdentifier)
	if err != nil {
		return StatusPageSectionResponse{}, err
	}

	taken := func(ctx context.Context, slug string) (bool, error) {
		existing, errGet := s.db.GetStatusPageSectionBySlug(ctx, page.UID, slug)
		if errGet != nil && !errors.Is(errGet, sql.ErrNoRows) {
			return false, errGet
		}

		return existing != nil, nil
	}

	slug, err := s.resolveCreateSlug(ctx, req.Slug, req.Name, slugFallbackSection, taken)
	if err != nil {
		return StatusPageSectionResponse{}, err
	}

	position, err := s.resolveSectionPosition(ctx, page.UID, req.Position)
	if err != nil {
		return StatusPageSectionResponse{}, err
	}

	section := models.NewStatusPageSection(page.UID, req.Name, slug, position)

	if errCreate := s.db.CreateStatusPageSection(ctx, section); errCreate != nil {
		return StatusPageSectionResponse{}, errCreate
	}

	return convertSectionToResponse(section), nil
}

// GetSection retrieves a single section.
func (s *Service) GetSection(
	ctx context.Context, orgSlug, pageIdentifier, sectionIdentifier string,
) (StatusPageSectionResponse, error) {
	page, err := s.resolveStatusPage(ctx, orgSlug, pageIdentifier)
	if err != nil {
		return StatusPageSectionResponse{}, err
	}

	section, err := s.resolveSection(ctx, page.UID, sectionIdentifier)
	if err != nil {
		return StatusPageSectionResponse{}, err
	}

	return convertSectionToResponse(section), nil
}

// UpdateSection updates an existing section.
func (s *Service) UpdateSection(
	ctx context.Context, orgSlug, pageIdentifier, sectionIdentifier string, req UpdateSectionRequest,
) (StatusPageSectionResponse, error) {
	page, err := s.resolveStatusPage(ctx, orgSlug, pageIdentifier)
	if err != nil {
		return StatusPageSectionResponse{}, err
	}

	section, err := s.resolveSection(ctx, page.UID, sectionIdentifier)
	if err != nil {
		return StatusPageSectionResponse{}, err
	}

	// Validate slug if provided
	if req.Slug != nil && *req.Slug != "" && *req.Slug != section.Slug {
		if errSlug := validateSlug(*req.Slug); errSlug != nil {
			return StatusPageSectionResponse{}, errSlug
		}
		existing, errCheck := s.db.GetStatusPageSectionBySlug(ctx, page.UID, *req.Slug)
		if errCheck != nil && !errors.Is(errCheck, sql.ErrNoRows) {
			return StatusPageSectionResponse{}, errCheck
		}
		if existing != nil {
			return StatusPageSectionResponse{}, ErrSlugConflict
		}
	}

	update := models.StatusPageSectionUpdate{
		Name:     req.Name,
		Slug:     req.Slug,
		Position: req.Position,
	}

	if errUpdate := s.db.UpdateStatusPageSection(ctx, section.UID, &update); errUpdate != nil {
		return StatusPageSectionResponse{}, errUpdate
	}

	updated, err := s.db.GetStatusPageSection(ctx, page.UID, section.UID)
	if err != nil {
		return StatusPageSectionResponse{}, err
	}

	return convertSectionToResponse(updated), nil
}

// DeleteSection soft-deletes a section.
func (s *Service) DeleteSection(
	ctx context.Context, orgSlug, pageIdentifier, sectionIdentifier string,
) error {
	page, err := s.resolveStatusPage(ctx, orgSlug, pageIdentifier)
	if err != nil {
		return err
	}

	section, err := s.resolveSection(ctx, page.UID, sectionIdentifier)
	if err != nil {
		return err
	}

	return s.db.DeleteStatusPageSection(ctx, section.UID)
}

// --- Resource CRUD ---

// ListResources retrieves all resources for a section.
func (s *Service) ListResources(
	ctx context.Context, orgSlug, pageIdentifier, sectionIdentifier string,
) ([]StatusPageResourceResponse, error) {
	page, err := s.resolveStatusPage(ctx, orgSlug, pageIdentifier)
	if err != nil {
		return nil, err
	}

	section, err := s.resolveSection(ctx, page.UID, sectionIdentifier)
	if err != nil {
		return nil, err
	}

	resources, err := s.db.ListStatusPageResources(ctx, section.UID)
	if err != nil {
		return nil, err
	}

	responses := make([]StatusPageResourceResponse, len(resources))
	for i, resource := range resources {
		responses[i] = convertResourceToResponse(resource)
	}

	return responses, nil
}

// resolveResourceTarget validates the checkUid-XOR-checkGroupUid pair on a
// resource write and resolves the supplied identifier (UID or slug) to a
// canonical UID. Exactly one of the two must be non-empty; zero or both is
// ErrResourceTargetInvalid, whose message names both fields (spec
// 2026-08-01-03). Returns (checkUID, checkGroupUID) with exactly one set.
func (s *Service) resolveResourceTarget(
	ctx context.Context, orgUID, checkIdentifier, groupIdentifier string,
) (*string, *string, error) {
	hasCheck := checkIdentifier != ""
	hasGroup := groupIdentifier != ""

	if hasCheck == hasGroup {
		return nil, nil, ErrResourceTargetInvalid
	}

	if hasCheck {
		check, err := s.db.GetCheckByUidOrSlug(ctx, orgUID, checkIdentifier)
		if err != nil || check == nil {
			return nil, nil, ErrCheckNotFound
		}

		return &check.UID, nil, nil
	}

	group, err := s.db.GetCheckGroupByUidOrSlug(ctx, orgUID, groupIdentifier)
	if err != nil || group == nil {
		return nil, nil, ErrCheckGroupNotFound
	}

	return nil, &group.UID, nil
}

// CreateResource adds a check or a check group to a section.
func (s *Service) CreateResource(
	ctx context.Context, orgSlug, pageIdentifier, sectionIdentifier string, req CreateResourceRequest,
) (StatusPageResourceResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return StatusPageResourceResponse{}, ErrOrganizationNotFound
	}

	page, err := s.db.GetStatusPageByUidOrSlug(ctx, org.UID, pageIdentifier)
	if err != nil || page == nil {
		return StatusPageResourceResponse{}, ErrStatusPageNotFound
	}

	section, err := s.resolveSection(ctx, page.UID, sectionIdentifier)
	if err != nil {
		return StatusPageResourceResponse{}, err
	}

	// Verify the target exists in this org, and that exactly one kind was given.
	checkUID, groupUID, err := s.resolveResourceTarget(ctx, org.UID, req.CheckUID, req.CheckGroupUID)
	if err != nil {
		return StatusPageResourceResponse{}, err
	}

	position, err := s.resolveResourcePosition(ctx, section.UID, req.Position)
	if err != nil {
		return StatusPageResourceResponse{}, err
	}

	var resource *models.StatusPageResource
	if checkUID != nil {
		resource = models.NewStatusPageResource(section.UID, *checkUID, position)
	} else {
		resource = models.NewStatusPageGroupResource(section.UID, *groupUID, position)
	}

	resource.PublicName = req.PublicName
	resource.Explanation = req.Explanation
	resource.AutoPublish = req.AutoPublish

	if err := s.db.CreateStatusPageResource(ctx, resource); err != nil {
		return StatusPageResourceResponse{}, fmt.Errorf("failed to create resource: %w", err)
	}

	return convertResourceToResponse(resource), nil
}

// UpdateResource updates a resource. Supplying checkUid or checkGroupUid
// switches the target kind; supplying both is a validation error and supplying
// neither leaves the target untouched.
func (s *Service) UpdateResource(
	ctx context.Context, orgSlug, pageIdentifier, sectionIdentifier, resourceUID string,
	req UpdateResourceRequest,
) (StatusPageResourceResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return StatusPageResourceResponse{}, ErrOrganizationNotFound
	}

	page, err := s.db.GetStatusPageByUidOrSlug(ctx, org.UID, pageIdentifier)
	if err != nil || page == nil {
		return StatusPageResourceResponse{}, ErrStatusPageNotFound
	}

	section, err := s.resolveSection(ctx, page.UID, sectionIdentifier)
	if err != nil {
		return StatusPageResourceResponse{}, err
	}

	update := models.StatusPageResourceUpdate{
		PublicName:     req.PublicName,
		SetAutoPublish: req.AutoPublishSet,
		AutoPublish:    req.AutoPublish,
		Explanation:    req.Explanation,
		Position:       req.Position,
	}

	if req.CheckUID != nil || req.CheckGroupUID != nil {
		checkUID, groupUID, errTarget := s.resolveResourceTarget(
			ctx, org.UID, derefOrEmpty(req.CheckUID), derefOrEmpty(req.CheckGroupUID),
		)
		if errTarget != nil {
			return StatusPageResourceResponse{}, errTarget
		}

		update.SetTarget = true
		update.CheckUID = checkUID
		update.CheckGroupUID = groupUID
	}

	if errUpdate := s.db.UpdateStatusPageResource(ctx, resourceUID, &update); errUpdate != nil {
		return StatusPageResourceResponse{}, errUpdate
	}

	updated, err := s.db.GetStatusPageResource(ctx, section.UID, resourceUID)
	if err != nil {
		return StatusPageResourceResponse{}, err
	}

	return convertResourceToResponse(updated), nil
}

// ReorderResources rewrites the section's resources so that orderedUIDs[i]
// gets position i+1. Validates that orderedUIDs is exactly the current set
// of resources in the section before applying — partial or stale orderings
// are rejected so the dashboard can't accidentally drop or duplicate rows.
func (s *Service) ReorderResources(
	ctx context.Context, orgSlug, pageIdentifier, sectionIdentifier string, orderedUIDs []string,
) error {
	page, err := s.resolveStatusPage(ctx, orgSlug, pageIdentifier)
	if err != nil {
		return err
	}

	section, err := s.resolveSection(ctx, page.UID, sectionIdentifier)
	if err != nil {
		return err
	}

	existing, err := s.db.ListStatusPageResources(ctx, section.UID)
	if err != nil {
		return fmt.Errorf("failed to list resources: %w", err)
	}
	if len(existing) != len(orderedUIDs) {
		return ErrReorderUIDsMismatch
	}
	known := make(map[string]struct{}, len(existing))
	for _, r := range existing {
		known[r.UID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(orderedUIDs))
	for _, uid := range orderedUIDs {
		if _, ok := known[uid]; !ok {
			return ErrReorderUIDsMismatch
		}
		if _, dup := seen[uid]; dup {
			return ErrReorderUIDsMismatch
		}
		seen[uid] = struct{}{}
	}

	return s.db.ReorderStatusPageResources(ctx, section.UID, orderedUIDs)
}

// ReorderSections rewrites the page's sections so that orderedUIDs[i] gets
// position i+1. Validates that orderedUIDs is exactly the current set of
// sections on the page before applying — partial or stale orderings are
// rejected so the dashboard can't accidentally drop or duplicate sections.
func (s *Service) ReorderSections(
	ctx context.Context, orgSlug, pageIdentifier string, orderedUIDs []string,
) error {
	page, err := s.resolveStatusPage(ctx, orgSlug, pageIdentifier)
	if err != nil {
		return err
	}

	existing, err := s.db.ListStatusPageSections(ctx, page.UID)
	if err != nil {
		return fmt.Errorf("failed to list sections: %w", err)
	}
	if len(existing) != len(orderedUIDs) {
		return ErrReorderUIDsMismatch
	}
	known := make(map[string]struct{}, len(existing))
	for _, sec := range existing {
		known[sec.UID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(orderedUIDs))
	for _, uid := range orderedUIDs {
		if _, ok := known[uid]; !ok {
			return ErrReorderUIDsMismatch
		}
		if _, dup := seen[uid]; dup {
			return ErrReorderUIDsMismatch
		}
		seen[uid] = struct{}{}
	}

	return s.db.ReorderStatusPageSections(ctx, page.UID, orderedUIDs)
}

// DeleteResource removes a check from a section (hard delete).
func (s *Service) DeleteResource(
	ctx context.Context, orgSlug, pageIdentifier, sectionIdentifier, resourceUID string,
) error {
	page, err := s.resolveStatusPage(ctx, orgSlug, pageIdentifier)
	if err != nil {
		return err
	}

	if _, err := s.resolveSection(ctx, page.UID, sectionIdentifier); err != nil {
		return err
	}

	return s.db.DeleteStatusPageResource(ctx, resourceUID)
}

// --- White label (spec 2026-08-21-07) ---

// whiteLabelAllowed reports the org's `whiteLabel` entitlement.
//
// It FAILS CLOSED in both directions that can go wrong: a nil entitlements
// service (the MCP wiring and most unit tests) and a resolve error both mean
// "keep the badge". Showing the badge when it should have been hidden is a
// cosmetic annoyance; hiding it when the plan does not include it is a silent
// revenue leak, so the asymmetry is deliberate.
func (s *Service) whiteLabelAllowed(ctx context.Context, orgUID string) bool {
	if s.ent == nil {
		return false
	}

	return s.ent.WhiteLabelAllowed(ctx, orgUID)
}

// enrichAdminBranding annotates an ADMIN payload with the entitlement, leaving
// HideBranding as the raw stored opt-in. The dashboard needs both halves: the
// toggle reflects what is stored, and the note next to it explains whether it
// currently does anything.
func (s *Service) enrichAdminBranding(resp *StatusPageResponse, allowed bool) {
	resp.WhiteLabelAllowed = &allowed
}

// resolvePublicBranding collapses the two halves into the single boolean the
// public payload carries: the badge disappears only when the page opted in AND
// the org is entitled. WhiteLabelAllowed is stripped — which plan an
// organization pays for is nobody else's business.
func (s *Service) resolvePublicBranding(ctx context.Context, orgUID string, resp *StatusPageResponse) {
	resp.HideBranding = resp.HideBranding && s.whiteLabelAllowed(ctx, orgUID)
	resp.WhiteLabelAllowed = nil
	// Strip the admin-only kiosk flag (spec 2026-08-29-08). Whether a page has
	// a wallboard token is operator information: publishing it would tell an
	// anonymous reader that a second way in exists and is worth guessing at.
	resp.HasKioskToken = false
}

// --- Public view ---

// publicAccessError maps the shared public-access gate onto this package's
// errors. It exists so ViewStatusPage and ViewStatusPageSummary cannot drift
// apart on who may read a page — and so the kiosk token, the unlock cookie and
// the visibility column are all considered in ONE place.
func publicAccessError(ctx context.Context, page *models.StatusPage) error {
	switch statuspagekiosk.Decide(ctx, page) {
	case statuspagekiosk.DecisionNotFound:
		return ErrStatusPageNotFound
	case statuspagekiosk.DecisionLocked:
		return statuspagelock.ErrLocked
	case statuspagekiosk.DecisionAllow:
		return nil
	default:
		return ErrStatusPageNotFound
	}
}

// ViewStatusPage returns a public view of a status page with sections, resources, and live check status.
func (s *Service) ViewStatusPage(
	ctx context.Context, orgSlug, slug string,
) (StatusPageResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return StatusPageResponse{}, ErrOrganizationNotFound
	}

	page, err := s.db.GetStatusPageBySlug(ctx, org.UID, slug)
	if err != nil || page == nil {
		return StatusPageResponse{}, ErrStatusPageNotFound
	}

	// One gate for every public surface (statuspagekiosk.Decide): disabled or
	// `private` is indistinguishable from not existing, a `password` page is
	// KNOWN to exist and answers 401, and a valid kiosk token overrides both
	// for the one screen holding it. An invalid token is byte-identical to no
	// token.
	if err := publicAccessError(ctx, page); err != nil {
		return StatusPageResponse{}, err
	}

	response := convertPageToResponse(page)
	s.resolvePublicBranding(ctx, org.UID, &response)

	sections, err := s.loadSectionsWithResources(ctx, page.UID)
	if err != nil {
		return StatusPageResponse{}, err
	}

	// Enrich resources with live check / group data, and compute the
	// page-level rollup from that same live data (spec 2026-08-08-05).
	overallStatus, statusCounts := s.enrichResourceInfo(ctx, org.UID, sections)
	response.OverallStatus = string(overallStatus)
	response.StatusCounts = &StatusCountsResponse{
		Operational: statusCounts.Operational,
		Degraded:    statusCounts.Degraded,
		Down:        statusCounts.Down,
		Maintenance: statusCounts.Maintenance,
		Unknown:     statusCounts.Unknown,
	}

	// Enrich resources with availability data
	if page.ShowAvailability || page.ShowResponseTime {
		s.enrichWithAvailability(ctx, org.UID, page, sections)
	}

	// Page-level uptime for the same window (spec 2026-08-29-08). Derived from
	// the per-resource numbers just computed, so it is free here and can never
	// disagree with the rows it summarizes. Gated on ShowAvailability: a page
	// whose operator chose not to publish per-resource uptime must not publish
	// the aggregate either.
	if page.ShowAvailability {
		response.OverallAvailabilityPct = meanResourceAvailability(sections)
	}

	response.Sections = sections

	// Active incident publications drive the banner and the active-incidents
	// section. Failing to load them must never blank the page — a status page
	// that cannot render its incidents is still far more useful than a 500.
	if s.publicIncidents != nil {
		active, incErr := s.publicIncidents.ListPublicIncidents(ctx, page, true)
		if incErr != nil {
			slog.ErrorContext(ctx, "Failed to load active incident publications for status page",
				"error", incErr, "statusPageUID", page.UID, "orgUID", org.UID)
		} else {
			response.ActiveIncidents = active
		}
	}

	response.RecentUpdates = s.loadRecentUpdates(ctx, org.UID, page)

	return response, nil
}

// loadRecentUpdates fetches the page's public status-update timeline.
//
// It never fails the page: a broken updates query used to silently render an
// empty timeline, which masked the Postgres `$1`-placeholder bug for both this
// page and the Atom feed, so the error is LOGGED rather than swallowed —
// it can't hide again, and a status page that cannot render its timeline is
// still far more useful than a 500.
func (s *Service) loadRecentUpdates(
	ctx context.Context, orgUID string, page *models.StatusPage,
) []StatusUpdatePublicResponse {
	if page.HistoryDays <= 0 {
		return nil
	}

	updates, err := s.db.ListPublicStatusUpdates(ctx, page.UID, page.HistoryDays)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to load public status updates for status page",
			"error", err, "statusPageUID", page.UID, "orgUID", orgUID)

		return nil
	}

	if len(updates) == 0 {
		return nil
	}

	recentUpdates := make([]StatusUpdatePublicResponse, len(updates))
	for i, upd := range updates {
		recentUpdates[i] = StatusUpdatePublicResponse{
			UID:          upd.UID,
			SectionUID:   upd.SectionUID,
			CheckUID:     upd.CheckUID,
			IncidentUID:  upd.IncidentUID,
			Title:        upd.Title,
			BodyMarkdown: upd.BodyMarkdown,
			LinkURL:      upd.LinkURL,
			Kind:         upd.Kind,
			PublishedAt:  upd.PublishedAt,
		}
	}

	return recentUpdates
}

// ViewDefaultStatusPage returns the default status page for an organization.
func (s *Service) ViewDefaultStatusPage(
	ctx context.Context, orgSlug string,
) (StatusPageResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return StatusPageResponse{}, ErrOrganizationNotFound
	}

	page, err := s.db.GetDefaultStatusPage(ctx, org.UID)
	if err != nil || page == nil {
		return StatusPageResponse{}, ErrStatusPageNotFound
	}

	return s.ViewStatusPage(ctx, orgSlug, page.Slug)
}

// ViewStatusPageSummary returns the lightweight page-level rollup for a
// status page (spec 2026-08-08-06) — the cheap "is it up?" companion to
// ViewStatusPage. It applies the IDENTICAL visibility gate (disabled or
// non-public → ErrStatusPageNotFound, same as ViewStatusPage) and computes the
// rollup from the exact same live data via models.RollupPageStatus, but skips
// the availability/response-time enrichment and recent-updates lookup that
// make the full page view expensive.
func (s *Service) ViewStatusPageSummary(
	ctx context.Context, orgSlug, slug string,
) (StatusPageSummary, error) {
	return s.viewStatusPageSummary(ctx, orgSlug, slug, true)
}

// viewStatusPageSummary is ViewStatusPageSummary with one extra knob:
// withAvailability decides whether the page-level uptime percentage (spec
// 2026-08-29-08) is computed.
//
// It is a knob rather than an unconditional addition because the aggregate is
// NOT free — it costs the same bucket query the full page view pays — and the
// summary has a second consumer with nowhere to put a percentage: the SVG
// badge (GenerateBadge), which is embedded in READMEs and is the hottest
// caller of this method. So the JSON summary endpoint asks for it and the
// badge does not.
func (s *Service) viewStatusPageSummary(
	ctx context.Context, orgSlug, slug string, withAvailability bool,
) (StatusPageSummary, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return StatusPageSummary{}, ErrOrganizationNotFound
	}

	page, err := s.db.GetStatusPageBySlug(ctx, org.UID, slug)
	if err != nil || page == nil {
		return StatusPageSummary{}, ErrStatusPageNotFound
	}

	if err := publicAccessError(ctx, page); err != nil {
		return StatusPageSummary{}, err
	}

	sections, err := s.loadSectionsWithResources(ctx, page.UID)
	if err != nil {
		return StatusPageSummary{}, err
	}

	status, counts := s.enrichResourceInfo(ctx, org.UID, sections)

	customDomain := ""
	if page.CustomDomain != nil && page.CustomDomainVerifiedAt != nil {
		customDomain = *page.CustomDomain
	}

	return StatusPageSummary{
		Status:                 status,
		Counts:                 counts,
		PageName:               page.Name,
		PageSlug:               page.Slug,
		CustomDomain:           customDomain,
		Visibility:             page.Visibility,
		OverallAvailabilityPct: s.summaryAvailability(ctx, org.UID, page, sections, withAvailability),
	}, nil
}

// summaryAvailability computes the page-level uptime for the summary endpoint.
//
// It runs the SAME enrichment the full page view runs, with response time
// forced off, and then the SAME mean. Reimplementing a cheaper aggregate here
// is exactly how two numbers on two surfaces start disagreeing about the same
// page, so the duplication is avoided rather than optimized.
func (s *Service) summaryAvailability(
	ctx context.Context, orgUID string, page *models.StatusPage,
	sections []StatusPageSectionResponse, withAvailability bool,
) *float64 {
	if !withAvailability || !page.ShowAvailability {
		return nil
	}

	// A shallow copy: enrichWithAvailability reads ShowResponseTime twice, and
	// the summary has no chart to feed — fetching a hundred raw result rows per
	// (check, region) for a number nobody renders is the one part of the full
	// view worth skipping here.
	lean := *page
	lean.ShowResponseTime = false

	s.enrichWithAvailability(ctx, orgUID, &lean, sections)

	return meanResourceAvailability(sections)
}

// --- Badge (spec 2026-08-08-07) ---

// Badge is a rendered page badge together with the visibility of the page it
// reflects. The SVG alone is not enough for the handler: the badge text
// discloses the rollup status of a page the requester may not be entitled to
// see, so the caching directive has to travel with it (spec 2026-08-22-06).
type Badge struct {
	SVG        string
	Visibility string
}

// BadgeOptions are the caller-supplied rendering options for the page-level
// SVG badge. Mirrors badges.BadgeOptions' Label/Style/MinWidth/Width shape so
// the two badge surfaces stay consistent, but is defined locally to avoid the
// statuspages package depending on badges' check-specific option set.
type BadgeOptions struct {
	Label    string // Custom left-side text (default: page name).
	Style    string // "flat", "flat-square".
	MinWidth int    // 0 = no minimum.
	Width    int    // Alternative width knob; the page badge has no bar/graph
	// rows, so this simply feeds the same single-row stretch as MinWidth
	// (the larger of the two wins) — see badges.Service.GenerateBadge's
	// text-only path for the sibling behavior.
}

// pageBadgeColor maps a page-level rollup status to the badge's fill color.
// The right-side text is the rollup status's own wire value (string(status)) —
// reusing models.PageStatus's vocabulary directly rather than a parallel set
// of string literals, so the badge's text can never drift from the
// summary/full-page JSON. English text in v1 (localization out of scope, per
// spec).
func pageBadgeColor(status models.PageStatus) string {
	switch status {
	case models.PageStatusOperational:
		return badges.ColorGreen
	case models.PageStatusDegraded:
		return badges.ColorYellow
	case models.PageStatusDown:
		return badges.ColorRed
	case models.PageStatusMaintenance:
		return badges.ColorBlue
	case models.PageStatusUnknown:
		return badges.ColorGray
	default:
		return badges.ColorGray
	}
}

// GenerateBadge renders the page-level SVG badge for a status page (spec
// 2026-08-08-07): a shields.io-style "page name | rollup status" badge for
// embedding in READMEs, wikis, or email footers. It resolves through
// ViewStatusPageSummary, so it applies the IDENTICAL visibility gate
// (disabled/non-public → ErrStatusPageNotFound) and the same
// models.RollupPageStatus rollup as the summary endpoint and the full status
// page — a badge that disagreed with either would be worse than no badge.
// Rendering itself reuses badges.GenerateSVG/ComposeBadgeSVG rather than a
// second SVG renderer.
func (s *Service) GenerateBadge(ctx context.Context, orgSlug, slug string, opts BadgeOptions) (Badge, error) {
	// withAvailability=false: a badge is "name | status" and has nowhere to put
	// a percentage, so it must not pay for one.
	summary, err := s.viewStatusPageSummary(ctx, orgSlug, slug, false)
	if err != nil {
		return Badge{}, err
	}

	label := opts.Label
	if label == "" {
		label = summary.PageName
	}

	style := opts.Style
	if style == "" {
		style = "flat"
	}

	minWidth := opts.MinWidth
	if opts.Width > minWidth {
		minWidth = opts.Width
	}

	value := string(summary.Status)
	color := pageBadgeColor(summary.Status)

	return Badge{
		SVG:        badges.GenerateSVG(label, value, color, style, minWidth),
		Visibility: summary.Visibility,
	}, nil
}

// --- Availability enrichment ---

// statusPagePeriodInfo returns the bucket period type, the number of buckets,
// and the bucket duration for a status-page history period. It mirrors the
// badges endpoint's uptimeBarPeriodInfo so the two surfaces bucket identically:
// 24h → (hour, 24, 1h); 7d/30d/90d → (day, N, 24h). An unknown value defaults
// to 90 daily buckets.
func statusPagePeriodInfo(period models.StatusPagePeriod) (string, int, time.Duration) {
	switch period {
	case models.StatusPagePeriod24h:
		return models.PeriodTypeHour, 24, time.Hour
	case models.StatusPagePeriod7d:
		return models.PeriodTypeDay, 7, 24 * time.Hour
	case models.StatusPagePeriod30d:
		return models.PeriodTypeDay, 30, 24 * time.Hour
	case models.StatusPagePeriod90d:
		return models.PeriodTypeDay, 90, 24 * time.Hour
	default:
		return models.PeriodTypeDay, 90, 24 * time.Hour
	}
}

// pagePeriod resolves a page's effective history period. It prefers the
// history_period enum; for legacy rows whose column is empty or invalid it
// falls back to mapping history_days.
func pagePeriod(page *models.StatusPage) models.StatusPagePeriod {
	p := models.StatusPagePeriod(page.HistoryPeriod)
	if p.Valid() {
		return p
	}

	return models.PeriodFromDays(page.HistoryDays)
}

// resourceMembers is the expansion of every resource on a page into the check
// UIDs whose results back it: one UID for a check resource, and the group's
// enabled non-deleted members for a group resource (spec 2026-08-01-03). It is
// keyed by resource UID.
type resourceMembers map[string][]string

// expandResourceMembers builds the resource → member check UIDs map and the
// deduplicated union of every check UID involved, so the whole page still costs
// exactly ONE uptimebar.BucketAvailability query. A group whose members can't
// be listed simply expands to nothing and renders as "no data" rather than
// failing the page.
func (s *Service) expandResourceMembers(
	ctx context.Context, orgUID string, sections []StatusPageSectionResponse,
) (resourceMembers, []string) {
	members := make(resourceMembers)
	seen := make(map[string]struct{})

	var union []string

	add := func(uid string) {
		if _, dup := seen[uid]; dup {
			return
		}

		seen[uid] = struct{}{}
		union = append(union, uid)
	}

	for i := range sections {
		for j := range sections[i].Resources {
			resource := &sections[i].Resources[j]

			switch {
			case resource.CheckGroupUID != nil:
				uids, err := s.db.ListCheckUIDsByGroup(ctx, orgUID, *resource.CheckGroupUID)
				if err != nil {
					slog.ErrorContext(ctx, "Failed to list check group members for status page",
						"error", err, "orgUID", orgUID, "checkGroupUID", *resource.CheckGroupUID)

					continue
				}

				members[resource.UID] = uids

				for _, uid := range uids {
					add(uid)
				}
			case resource.CheckUID != nil:
				members[resource.UID] = []string{*resource.CheckUID}

				add(*resource.CheckUID)
			}
		}
	}

	return members, union
}

// mergeBuckets sums the per-bucket stats of several checks into one series.
// Summing Up and Total (rather than averaging percentages) is exactly the
// weighted average the single-check path already computes — sum of successful
// over sum of total — so a group and a check share one formula. A member with
// no rows in a bucket contributes nothing to that bucket, which is not the same
// as contributing zero.
func mergeBuckets(
	bucketsByCheck map[string]map[time.Time]uptimebar.BucketStats, checkUIDs []string,
) map[time.Time]uptimebar.BucketStats {
	if len(checkUIDs) == 1 {
		return bucketsByCheck[checkUIDs[0]]
	}

	merged := make(map[time.Time]uptimebar.BucketStats)

	for _, checkUID := range checkUIDs {
		byBucket := bucketsByCheck[checkUID]
		for bucket := range byBucket {
			stats := byBucket[bucket]
			acc := merged[bucket]
			acc.Add(stats)
			merged[bucket] = acc
		}
	}

	return merged
}

func (s *Service) enrichWithAvailability(
	ctx context.Context, orgUID string, page *models.StatusPage, sections []StatusPageSectionResponse,
) {
	members, checkUIDs := s.expandResourceMembers(ctx, orgUID, sections)

	if len(checkUIDs) == 0 {
		return
	}

	// 24h renders 24 hourly buckets — a separate code path from the daily mode,
	// mirroring the badges period=24h hourly bucketing.
	if pagePeriod(page).IsHourly() {
		s.enrichHourly(ctx, orgUID, page, sections, members, checkUIDs)

		return
	}

	// Bucket daily availability from the shared raw+hour+day union, exactly like
	// the badge: every day with any row (raw OR rollup) is filled, so a recent day
	// whose raw rows haven't been rolled up yet no longer reads "No data".
	// historyStart matches the loop in buildAvailabilityData: the oldest of the
	// HistoryDays buckets is (HistoryDays-1) days before today (UTC midnight).
	now := time.Now().UTC()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	historyStart := todayStart.AddDate(0, 0, -(page.HistoryDays - 1))

	hints := s.uptimebarHints(ctx, orgUID)

	bucketsByCheck, err := uptimebar.BucketAvailability(
		ctx, s.db, orgUID, checkUIDs, 24*time.Hour, historyStart, page.HistoryDays,
		hints,
	)
	if err != nil {
		return
	}

	recentByCheck := s.fetchRecentResults(ctx, orgUID, checkUIDs, page.ShowResponseTime, hints)

	// Build availability data for each resource
	period := string(pagePeriod(page))
	upThreshold, degradedThreshold := page.Settings.EffectiveThresholds()

	for i := range sections {
		for j := range sections[i].Resources {
			resource := &sections[i].Resources[j]
			memberUIDs := members[resource.UID]
			availData := buildAvailabilityData(
				mergeBuckets(bucketsByCheck, memberUIDs), resourceRecentResults(recentByCheck, resource, memberUIDs),
				todayStart, page.HistoryDays, page.ShowAvailability, page.ShowResponseTime,
				upThreshold, degradedThreshold,
			)
			availData.Period = period
			availData.BucketUnit = models.PeriodTypeDay
			resource.Availability = availData
		}
	}
}

// meanResourceAvailability is the PAGE-level uptime number (spec
// 2026-08-29-08): the arithmetic mean of the per-resource
// `overallAvailabilityPct` values already computed for this page's history
// period.
//
// The mean, not a time-weighted union of every probe. Both are defensible; the
// mean wins on the one property that matters for a number printed a metre high
// on an office wall — a reader can verify it. Every per-resource percentage is
// on the page directly under it, and their average is a sum a person can do in
// their head. A union ("any resource down = the page is down") is stricter and
// arguably truer to how customers experience an outage, but it cannot be
// reconciled with the rows above it without explaining probe weighting, so it
// would read as a number that disagrees with the page it sits on. The docs
// (web/docs/docs/features/status-page-tv-mode.md) state the definition.
//
// Resources with NO data are EXCLUDED rather than counted as 0 % or 100 %:
// a check that has never reported is not an outage, and it is not perfect
// uptime either. Returns nil when nothing has data — the caller omits the
// field, and the board shows no number rather than a confident lie.
func meanResourceAvailability(sections []StatusPageSectionResponse) *float64 {
	var (
		sum   float64
		count int
	)

	for i := range sections {
		for j := range sections[i].Resources {
			avail := sections[i].Resources[j].Availability
			if avail == nil || avail.OverallAvailabilityPct == nil {
				continue
			}

			sum += *avail.OverallAvailabilityPct
			count++
		}
	}

	if count == 0 {
		return nil
	}

	mean := sum / float64(count)

	return &mean
}

// resourceRecentResults returns the per-region response-time results for a
// resource, keyed by region ("" for a NULL/legacy region).
//
// A GROUP resource gets NO series: interleaving several members' p95 points
// into one chart produces a meaningless plot, and it would publish per-member
// latency — the one thing a group component exists to hide. The aggregated
// availability bar is the group's public performance surface (spec
// 2026-08-01-03).
func resourceRecentResults(
	recentByCheck map[string]map[string][]*models.Result, resource *StatusPageResourceResponse, memberUIDs []string,
) map[string][]*models.Result {
	if resource.CheckGroupUID != nil || len(memberUIDs) != 1 {
		return nil
	}

	return recentByCheck[memberUIDs[0]]
}

// regionFanoutCap generously bounds the number of distinct regions
// fetchRecentResults assumes for a check whose own region set it cannot see.
// Real deployments run a handful of regions (2-5 is typical for a multi-region
// check); this mirrors uptimebar.capMaxRegionsPerCheck's "generous, never bites
// under realistic topology" reasoning for the same class of problem.
//
// It used to multiply a GLOBAL row limit (responseTimeLimit x regionFanoutCap x
// len(checkUIDs) = 40 000 rows for a 20-check page, ~85 % of them discarded).
// It is now only the per-check fallback for an unknown region set: per-check
// isolation comes from the query shape itself (spec 2026-08-22-05).
const regionFanoutCap = 20

// responseTimeLimit is the number of points the response-time chart keeps per
// (check, region) pair.
const responseTimeLimit = 100

// responseTimeRollupSpan is how far back the ROLLUP branch reads:
// responseTimeLimit day-buckets, doubled for slack. That is the widest window
// the chart can meaningfully render — 100 points at the coarsest tier a status
// page has any business plotting — and it reaches comfortably past the default
// day-rollup retention (2 months), so the `month` tier still contributes the
// 60-200 day band rather than being requested and never matched.
//
// Rollups are cheap to bound generously: one row per bucket per tier per
// region, against raw's one row per probe. The raw branch is the expensive one
// and is clamped to actual raw retention instead (see uptimebar.RawTierStart).
const responseTimeRollupSpan = 2 * responseTimeLimit * 24 * time.Hour

// responseTimeRollupTiers is the ROLLUP half of the fetch. It names every
// aggregated tier and NO raw tier, which is what lets the whole branch ride
// results_aggregated_idx (partial on period_type != 'raw').
//
// The tiers matter for real pages, not just for completeness: the per-(check,
// region) budget is 100 points, so a 1-minute check fills it from ~100 minutes
// of raw, but a 1-hour check needs 100 hours — past the 24 h raw retention, so
// those points exist ONLY as hour rollups. Dropping this branch would silently
// empty the chart for every slow check.
func responseTimeRollupTiers() []string {
	return []string{models.PeriodTypeHour, models.PeriodTypeDay, models.PeriodTypeMonth}
}

// fetchRecentResults loads the last responseTimeLimit results per
// (check, region) pair for the response-time chart, newest first. Returns an
// empty map when the response-time chart is disabled. The region key is "" for
// a NULL/legacy region (spec 2026-08-14-04 — that budget is per region, so a
// check running in N regions gets 100 points each, not 100/N).
//
// It issues ONE query, with a per-check row budget and one tier-aligned,
// time-bounded branch per side of the raw/rollup index split (spec
// 2026-08-22-05). What it replaced had neither a period-type filter nor a time
// bound, so no index on `results` was eligible and every public page view cost
// a sequential scan of the largest table in the system plus an external merge
// sort to disk.
//
// hints carries the org's LIVE raw retention, resolved once per request through
// systemconfig by the caller's uptimebarHints. It must not be re-derived from
// cfg.Aggregation.RetentionRaw here: the Aggregation settings tab writes
// performance.* DB parameters that never reach the koanf struct, so a koanf
// reader clamping to 24 h while the job keeps 168 h would silently drop six
// days of raw rows that no rollup covers yet.
func (s *Service) fetchRecentResults(
	ctx context.Context, orgUID string, checkUIDs []string, showResponseTime bool, hints uptimebar.Hints,
) map[string]map[string][]*models.Result {
	recentByCheck := make(map[string]map[string][]*models.Result)

	if !showResponseTime || len(checkUIDs) == 0 {
		return recentByCheck
	}

	now := time.Now().UTC()
	rollupSince := now.Add(-responseTimeRollupSpan)

	filter := &models.RecentResultsPerCheckFilter{
		OrganizationUID: orgUID,
		CheckUIDs:       checkUIDs,
		Tiers: []models.RecentResultsTier{
			{
				PeriodTypes: []string{models.PeriodTypeRaw},
				// Exactly uptimebar's raw clamp, from the same resolved
				// retention: raw older than this has been rolled up and
				// deleted, so a wider bound only widens the index descent.
				Since: uptimebar.RawTierStart(rollupSince, now, hints.RetentionRawHours),
			},
			{PeriodTypes: responseTimeRollupTiers(), Since: rollupSince},
		},
		PerCheckLimits:       s.responseTimeBudgets(ctx, orgUID, checkUIDs),
		DefaultPerCheckLimit: responseTimeLimit * regionFanoutCap,
	}

	rows, err := s.db.RecentResultsPerCheck(sloghook.WithCallsite(ctx, "statuspages.recent_results"), filter)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to load status page response-time results",
			"error", err, "orgUID", orgUID, "checks", len(checkUIDs))

		return recentByCheck
	}

	for _, result := range rows {
		regionKey := ""
		if result.Region != nil {
			regionKey = *result.Region
		}

		byRegion := recentByCheck[result.CheckUID]
		if byRegion == nil {
			byRegion = make(map[string][]*models.Result)
			recentByCheck[result.CheckUID] = byRegion
		}

		byRegion[regionKey] = append(byRegion[regionKey], result)
	}

	trimResponseTimeSeries(recentByCheck)

	return recentByCheck
}

// trimResponseTimeSeries merges the two tier branches into one per-(check,
// region) series: newest period_start first, preferring the FINER tier on a
// tie, truncated to responseTimeLimit points.
//
// Raw and rollups are disjoint by construction — the aggregation job compacts a
// bucket and deletes its source raw rows in one transaction — so in practice
// raw simply occupies the recent end and rollups fill the older tail. The
// raw-wins tie-break only matters while the aggregation job is mid-rollup, and
// it resolves that window in favor of the more precise row.
//
// A third, final rung breaks ties between two ROLLUP points that share a
// period_start — e.g. the hour bucket at midnight and the day bucket for that
// same day, which happens daily (all three tiers collide at a month
// boundary). Without it, sort.SliceStable falls back to whatever order the DB
// happened to return, which is nondeterministic. uid DESC restores the
// determinism the pre-2026-08-22-05 query guaranteed
// (ORDER BY period_start DESC, uid DESC), so it stays total (UIDs are unique)
// without disturbing the raw-wins rung above it.
func trimResponseTimeSeries(recentByCheck map[string]map[string][]*models.Result) {
	for _, byRegion := range recentByCheck {
		for regionKey, rows := range byRegion {
			sort.SliceStable(rows, func(i, j int) bool {
				if !rows[i].PeriodStart.Equal(rows[j].PeriodStart) {
					return rows[i].PeriodStart.After(rows[j].PeriodStart)
				}

				iRaw := rows[i].PeriodType == models.PeriodTypeRaw
				jRaw := rows[j].PeriodType == models.PeriodTypeRaw

				if iRaw != jRaw {
					return iRaw
				}

				return rows[i].UID > rows[j].UID // uid DESC — matches the pre-2026-08-22-05 query
			})

			if len(rows) > responseTimeLimit {
				rows = rows[:responseTimeLimit]
			}

			byRegion[regionKey] = rows
		}
	}
}

// responseTimeBudgets sizes each check's row budget from ITS OWN region
// fan-out: responseTimeLimit points per configured region, plus one region's
// worth of slack for the NULL/legacy region bucket — a check created before
// regions existed carries a NULL region, and so does every check's one-time
// "Check created" lifecycle marker, which would otherwise eat a point out of a
// real region's budget.
//
// The budget has to cover every region at once because the branch is ordered by
// period_start across the whole check, not per region: a 3-region check's
// newest 100 rows would be ~33 points each. It is capped at the regionFanoutCap
// product so a check configured with an absurd region list cannot reintroduce
// an unbounded fetch.
//
// A check with an EMPTY region list gets no entry at all, and so falls back to
// DefaultPerCheckLimit (the pre-fix per-check budget). An empty list does not
// mean "one region": createCheckJobs turns it into ONE region-less job, and
// CheckWorker.resolveResultRegion then stamps that result with the WORKER's own
// region — so a region-less check's rows can carry as many different region
// labels as there are workers that ever claimed it. The fan-out is genuinely
// unknown, and only the generous cap is safe.
//
// That path is rare in production: every check created or patched through the
// API has its regions resolved and PERSISTED
// (checks.Service.CreateCheck -> regions.ResolveRegionsForCheck, which never
// returns an empty list). An empty column means a check written straight to the
// database — fixtures, seeds, very old rows. Same fallback for a check that
// vanished between resource expansion and this lookup, and for a failed lookup
// (every check then uses the default).
func (s *Service) responseTimeBudgets(ctx context.Context, orgUID string, checkUIDs []string) map[string]int {
	checks, err := s.db.GetChecksByUIDs(ctx, orgUID, checkUIDs)
	if err != nil {
		slog.WarnContext(ctx, "Failed to size status page response-time budgets from check regions; "+
			"falling back to the region fan-out cap",
			"error", err, "orgUID", orgUID)

		return nil
	}

	budgets := make(map[string]int, len(checkUIDs))

	for _, checkUID := range checkUIDs {
		check := checks[checkUID]
		if check == nil || len(check.Regions) == 0 {
			continue
		}

		budgets[checkUID] = responseTimeLimit * min(len(check.Regions)+1, regionFanoutCap)
	}

	return budgets
}

// hourlyBucketCount is the number of hourly buckets the 24h view renders.
const hourlyBucketCount = 24

// enrichHourly populates each resource's availability with 24 hourly buckets for
// the 24h period, bucketed from the shared raw+hour+day union (uptimebar) exactly
// like the badges period=24h path. A past hour with raw-only data (the raw→hour
// rollup lags by RetentionRaw) is filled from raw rather than reading "No data".
func (s *Service) enrichHourly(
	ctx context.Context, orgUID string, page *models.StatusPage,
	sections []StatusPageSectionResponse, members resourceMembers, checkUIDs []string,
) {
	now := time.Now().UTC()
	// The newest of the 24 buckets is the current, in-progress hour; the oldest is
	// 23 hours earlier. -(n-1) keeps the current hour inside the window.
	bucketStart := now.Truncate(time.Hour).Add(-time.Duration(hourlyBucketCount-1) * time.Hour)

	hints := s.uptimebarHints(ctx, orgUID)

	bucketsByCheck, err := uptimebar.BucketAvailability(
		ctx, s.db, orgUID, checkUIDs, time.Hour, bucketStart, hourlyBucketCount,
		hints,
	)
	if err != nil {
		return
	}

	recentByCheck := s.fetchRecentResults(ctx, orgUID, checkUIDs, page.ShowResponseTime, hints)
	upThreshold, degradedThreshold := page.Settings.EffectiveThresholds()

	for i := range sections {
		for j := range sections[i].Resources {
			resource := &sections[i].Resources[j]
			memberUIDs := members[resource.UID]
			availData := buildHourlyAvailabilityData(
				mergeBuckets(bucketsByCheck, memberUIDs),
				resourceRecentResults(recentByCheck, resource, memberUIDs), bucketStart,
				page.ShowAvailability, page.ShowResponseTime,
				upThreshold, degradedThreshold,
			)
			availData.Period = string(models.StatusPagePeriod24h)
			availData.BucketUnit = models.PeriodTypeHour
			resource.Availability = availData
		}
	}
}

// buildHourlyAvailabilityData builds the 24 hourly availability points anchored
// on bucketStart (oldest → newest, newest = current hour) from per-bucket stats.
// Buckets absent from byBucket (no rows) get status "noData". The overall
// weighted average matches the daily path.
func buildHourlyAvailabilityData(
	byBucket map[time.Time]uptimebar.BucketStats, recentResultsByRegion map[string][]*models.Result,
	bucketStart time.Time,
	showAvailability, showResponseTime bool, upThreshold, degradedThreshold float64,
) *ResourceAvailabilityData {
	data := &ResourceAvailabilityData{}

	if showAvailability {
		points := make([]AvailabilityPoint, 0, hourlyBucketCount)

		var (
			totalWeightedAvail float64
			totalChecksSum     int
		)

		for i := range hourlyBucketCount {
			bucket := bucketStart.Add(time.Duration(i) * time.Hour)

			point := AvailabilityPoint{
				Date:   bucket.Format("2006-01-02"),
				Time:   bucket.Format(time.RFC3339),
				Status: statusNoData,
			}

			if stats, ok := byBucket[bucket]; ok {
				if pct, hasData := stats.AvailabilityPct(); hasData {
					point.AvailabilityPct = pct
					point.Status = availabilityToStatus(pct, stats.Total-stats.Up, upThreshold, degradedThreshold)

					totalWeightedAvail += pct * float64(stats.Total)
					totalChecksSum += stats.Total
				}
			}

			points = append(points, point)
		}

		data.DailyAvailability = points

		if totalChecksSum > 0 {
			overall := totalWeightedAvail / float64(totalChecksSum)
			data.OverallAvailabilityPct = &overall
		}
	}

	if showResponseTime {
		data.ResponseTimeSeries = buildResponseTimeSeries(
			recentResultsByRegion, upThreshold, degradedThreshold)
	}

	return data
}

// buildAvailabilityData builds the daily availability points over the history
// window (oldest → newest, newest = today) from per-bucket stats keyed by the
// day's UTC-midnight start. Buckets absent from byBucket (no rows) get status
// "noData". todayStart is the UTC-midnight of the current day; the window's
// oldest bucket is (historyDays-1) days earlier, matching the BucketAvailability
// call in enrichWithAvailability.
func buildAvailabilityData(
	byBucket map[time.Time]uptimebar.BucketStats, recentResultsByRegion map[string][]*models.Result,
	todayStart time.Time, historyDays int, showAvailability, showResponseTime bool,
	upThreshold, degradedThreshold float64,
) *ResourceAvailabilityData {
	data := &ResourceAvailabilityData{}

	if showAvailability {
		daily := make([]AvailabilityPoint, 0, historyDays)

		var totalWeightedAvail float64

		var totalChecksSum int

		for dayOffset := historyDays - 1; dayOffset >= 0; dayOffset-- {
			day := todayStart.AddDate(0, 0, -dayOffset)
			dateStr := day.Format("2006-01-02")

			point := AvailabilityPoint{
				Date:   dateStr,
				Time:   day.Format(time.RFC3339),
				Status: statusNoData,
			}

			if stats, ok := byBucket[day]; ok {
				if pct, hasData := stats.AvailabilityPct(); hasData {
					point.AvailabilityPct = pct
					point.Status = availabilityToStatus(pct, stats.Total-stats.Up, upThreshold, degradedThreshold)

					totalWeightedAvail += pct * float64(stats.Total)
					totalChecksSum += stats.Total
				}
			}

			daily = append(daily, point)
		}

		data.DailyAvailability = daily

		if totalChecksSum > 0 {
			overall := totalWeightedAvail / float64(totalChecksSum)
			data.OverallAvailabilityPct = &overall
		}
	}

	if showResponseTime {
		data.ResponseTimeSeries = buildResponseTimeSeries(
			recentResultsByRegion, upThreshold, degradedThreshold)
	}

	return data
}

// buildResponseTimeSeries groups a resource's recent results into one series
// per region (spec 2026-08-14-04), replacing the old single-series flattening
// that interleaved every region's points. Region keys are sorted so the
// output order is stable; the empty-string key (NULL/legacy region) sorts
// first automatically and needs no special-casing.
//
// A region group with NO real duration signal at all is dropped entirely —
// this matters because every check gets one lifecycle "created" marker result
// (CreateCheck) with no region and no duration. Pre-region-splitting, that
// marker was harmless: one more null-duration entry lost in a single combined
// list. Post-splitting it would otherwise manufacture a phantom one-point
// "unknown region" series (and legend entry) for every single-region check. A
// region group that mixes the marker with real data keeps it, exactly as the
// old single-series behavior always did.
func buildResponseTimeSeries(
	recentResultsByRegion map[string][]*models.Result, upThreshold, degradedThreshold float64,
) []ResponseTimeSeries {
	if len(recentResultsByRegion) == 0 {
		return nil
	}

	regionKeys := make([]string, 0, len(recentResultsByRegion))
	for key := range recentResultsByRegion {
		regionKeys = append(regionKeys, key)
	}

	sort.Strings(regionKeys)

	series := make([]ResponseTimeSeries, 0, len(regionKeys))

	for _, key := range regionKeys {
		points := buildResponseTimeData(recentResultsByRegion[key], upThreshold, degradedThreshold)
		if !responseTimePointsHaveSignal(points) {
			continue
		}

		var region *string
		if key != "" {
			regionValue := key
			region = &regionValue
		}

		series = append(series, ResponseTimeSeries{Region: region, Points: points})
	}

	if len(series) == 0 {
		return nil
	}

	return series
}

// responseTimePointsHaveSignal reports whether at least one point carries a
// real duration — false for a region group made up entirely of lifecycle
// markers (e.g. the check-creation "created" result) or other durationless
// rows.
func responseTimePointsHaveSignal(points []ResponseTimePoint) bool {
	for i := range points {
		if points[i].DurationP95 != nil {
			return true
		}
	}

	return false
}

func buildResponseTimeData(
	recentResults []*models.Result, upThreshold, degradedThreshold float64,
) []ResponseTimePoint {
	rtData := make([]ResponseTimePoint, 0, len(recentResults))

	for _, recentResult := range recentResults {
		var duration *float32
		if recentResult.DurationP95 != nil {
			duration = recentResult.DurationP95
		} else if recentResult.Duration != nil {
			duration = recentResult.Duration
		}

		var statusStr string
		if recentResult.Status != nil {
			statusStr = strings.ToLower(models.StatusToString(*recentResult.Status))
		}

		// The canonical fold, never a local status count: lifecycle markers and
		// abandoned attempts leave both numerator and denominator alone, and
		// warning counts as up.
		stats := uptimebar.StatsForResult(recentResult)

		point := ResponseTimePoint{
			Time:               recentResult.PeriodStart.UTC().Format(time.RFC3339),
			DurationP95:        duration,
			Status:             statusStr,
			TotalChecks:        stats.Total,
			SuccessfulChecks:   stats.Up,
			AvailabilityStatus: uptimebar.ClassifyStats(stats, upThreshold, degradedThreshold),
		}

		if pct, ok := stats.AvailabilityPct(); ok {
			point.AvailabilityPct = &pct
		}

		rtData = append(rtData, point)
	}

	for i, j := 0, len(rtData)-1; i < j; i, j = i+1, j-1 {
		rtData[i], rtData[j] = rtData[j], rtData[i]
	}

	return rtData
}

// The public status vocabulary. Both the live per-resource status (check or
// rolled-up group) and the per-bucket availability status are spoken in these
// words, so the front end has a single set of values to render.
const (
	// statusNoData is the availability-point status for a bucket that has no
	// rows in the shared raw+hour+day union — the front end renders it gray.
	statusNoData    = uptimebar.StatusNoData
	statusCreated   = "created"
	statusUp        = uptimebar.StatusUp
	statusWarning   = "warning"
	statusDegraded  = uptimebar.StatusDegraded
	statusDownValue = uptimebar.StatusDown
)

// availabilityToStatus classifies a bucket's availability percentage into the
// up/degraded/down wire vocabulary using the page's effective thresholds
// (upThreshold/degradedThreshold — resolved once per request via
// models.StatusPageSettings.EffectiveThresholds, nil-safe fallback to
// 99.9/99.0).
//
// Small-bucket calibration guard (spec 2026-08-03-01 §4): a bucket with
// exactly ONE failed sample never renders "down", only at worst "degraded" —
// red requires >= 2 failed samples. failures is stats.Total - stats.Up. This
// fixes the hourly "one failed minute = red hour" cliff without changing
// percentage-threshold behavior on buckets large enough that one sample can't
// swing the classification anyway.
//
// The rule itself lives in uptimebar.Classify — the same package that owns the
// counting rules every strip shares — so the status page and the dash0 chart
// availability strip cannot drift apart on the same numbers. (The badge SVG
// keeps its own four-tier scale on purpose; see uptimebar.Classify.) This
// wrapper stays because the local vocabulary constants below are what the rest
// of this file reads.
func availabilityToStatus(pct float64, failures int, upThreshold, degradedThreshold float64) string {
	return uptimebar.Classify(pct, failures, upThreshold, degradedThreshold)
}

// --- Helpers ---

func (s *Service) resolveStatusPage(
	ctx context.Context, orgSlug, identifier string,
) (*models.StatusPage, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	page, err := s.db.GetStatusPageByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || page == nil {
		return nil, ErrStatusPageNotFound
	}

	return page, nil
}

func (s *Service) resolveSection(
	ctx context.Context, pageUID, identifier string,
) (*models.StatusPageSection, error) {
	var section *models.StatusPageSection
	var err error

	if _, parseErr := uuid.Parse(identifier); parseErr == nil {
		section, err = s.db.GetStatusPageSection(ctx, pageUID, identifier)
	} else {
		section, err = s.db.GetStatusPageSectionBySlug(ctx, pageUID, identifier)
	}

	if err != nil || section == nil {
		return nil, ErrStatusPageSectionNotFound
	}

	return section, nil
}

func (s *Service) validatePageSlugChange(
	ctx context.Context, orgUID, currentSlug string, newSlug *string,
) error {
	if newSlug == nil || *newSlug == "" || *newSlug == currentSlug {
		return nil
	}

	if errSlug := validateSlug(*newSlug); errSlug != nil {
		return errSlug
	}

	existing, errCheck := s.db.GetStatusPageBySlug(ctx, orgUID, *newSlug)
	if errCheck != nil && !errors.Is(errCheck, sql.ErrNoRows) {
		return errCheck
	}

	if existing != nil {
		return ErrSlugConflict
	}

	return nil
}

func (s *Service) clearDefaultStatusPage(ctx context.Context, orgUID string) error {
	pages, err := s.db.ListStatusPages(ctx, orgUID)
	if err != nil {
		return err
	}

	for _, p := range pages {
		if p.IsDefault {
			falseVal := false
			if err := s.db.UpdateStatusPage(ctx, p.UID, &models.StatusPageUpdate{IsDefault: &falseVal}); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *Service) loadSectionsWithResources(
	ctx context.Context, pageUID string,
) ([]StatusPageSectionResponse, error) {
	sections, err := s.db.ListStatusPageSections(ctx, pageUID)
	if err != nil {
		return nil, err
	}

	responses := make([]StatusPageSectionResponse, len(sections))
	for i, section := range sections {
		responses[i] = convertSectionToResponse(section)

		resources, err := s.db.ListStatusPageResources(ctx, section.UID)
		if err != nil {
			return nil, err
		}

		resourceResponses := make([]StatusPageResourceResponse, len(resources))
		for j, resource := range resources {
			resourceResponses[j] = convertResourceToResponse(resource)
		}
		responses[i].Resources = resourceResponses
	}

	return responses, nil
}

// enrichResourceInfo fills each resource's Check block with live data: a check
// resource gets its check's own status, a group resource gets the group's
// rolled-up status (spec 2026-08-01-03). The per-group status counts are read
// ONCE per page rather than per resource. A lookup failure leaves Check nil,
// exactly as before — a broken target must never take the page down (it is
// still counted as an unknown-status resource in the page-level rollup below).
//
// It also computes and returns the page-level rollup (spec 2026-08-08-05)
// from the exact same live data, so the two can never disagree.
func (s *Service) enrichResourceInfo(
	ctx context.Context, orgUID string, sections []StatusPageSectionResponse,
) (models.PageStatus, models.PageStatusCounts) {
	var groupCounts map[string]map[models.CheckStatus]int

	if pageHasGroupResource(sections) {
		counts, err := s.db.GetCheckGroupStatusCounts(ctx, orgUID)
		if err != nil {
			slog.ErrorContext(ctx, "Failed to load check group status counts for status page",
				"error", err, "orgUID", orgUID)
		} else {
			groupCounts = counts
		}
	}

	resourceCount := 0
	for i := range sections {
		resourceCount += len(sections[i].Resources)
	}

	rollupInputs := make([]models.PageResourceStatus, 0, resourceCount)

	for i := range sections {
		for j := range sections[i].Resources {
			resource := &sections[i].Resources[j]

			switch {
			case resource.CheckGroupUID != nil:
				info, rawStatus, err := s.getGroupInfo(ctx, orgUID, *resource.CheckGroupUID, groupCounts)
				if err == nil {
					resource.Check = info
					rollupInputs = append(rollupInputs, models.PageResourceStatus{
						Status: rawStatus, InMaintenance: info.InMaintenance,
					})
				} else {
					rollupInputs = append(rollupInputs, models.PageResourceStatus{Status: models.CheckStatusCreated})
				}
			case resource.CheckUID != nil:
				info, rawStatus, err := s.getCheckInfo(ctx, orgUID, *resource.CheckUID)
				if err == nil {
					resource.Check = info
					rollupInputs = append(rollupInputs, models.PageResourceStatus{
						Status: rawStatus, InMaintenance: info.InMaintenance,
					})
				} else {
					rollupInputs = append(rollupInputs, models.PageResourceStatus{Status: models.CheckStatusCreated})
				}
			}
		}
	}

	return models.RollupPageStatus(rollupInputs)
}

// pageHasGroupResource reports whether any resource on the page targets a check
// group, so the group status-counts query is skipped entirely on the (common)
// check-only page.
func pageHasGroupResource(sections []StatusPageSectionResponse) bool {
	for i := range sections {
		for j := range sections[i].Resources {
			if sections[i].Resources[j].CheckGroupUID != nil {
				return true
			}
		}
	}

	return false
}

// publicCheckStatus maps an internal check status onto the vocabulary the
// public status page renders. It is shared by the check and the group path so
// a group's rolled-up status is spoken in exactly the same words as a check's.
func publicCheckStatus(status models.CheckStatus) string {
	switch status {
	case models.CheckStatusCreated:
		return statusCreated
	case models.CheckStatusUp:
		return statusUp
	case models.CheckStatusDown:
		return statusDownValue
	case models.CheckStatusValidating:
		// Validating is a transient internal state — the public status page
		// should still read "up" until the failure is confirmed and an
		// incident opens.
		return statusUp
	case models.CheckStatusWarning:
		// Live "up, but something to report" — surfaced amber on the public
		// page (counts as up for availability, but is not hidden like
		// validating: the operator deliberately flagged something).
		return statusWarning
	case models.CheckStatusDegraded:
		return statusDegraded
	default:
		return statusCreated
	}
}

// getCheckInfo returns the public live-data block for a CHECK resource, plus
// the check's raw (pre-publicCheckStatus) models.CheckStatus — the latter is
// the input the page-level rollup (models.RollupPageStatus) needs, since the
// public status string is a lossier, display-only vocabulary.
func (s *Service) getCheckInfo(
	ctx context.Context, orgUID, checkUID string,
) (*ResourceCheckInfo, models.CheckStatus, error) {
	check, err := s.db.GetCheck(ctx, orgUID, checkUID)
	if err != nil {
		return nil, models.CheckStatusCreated, err
	}

	// Flag the resource as under maintenance when the check sits inside an
	// active maintenance window right now. A lookup error must never take the
	// public page down, so we log nothing fatal and default to false.
	inMaintenance := false

	if windows, errMW := s.db.ListMaintenanceWindowsForCheck(ctx, checkUID); errMW == nil {
		inMaintenance = anyWindowActive(windows)
	}

	return &ResourceCheckInfo{
		Name:          check.Name,
		Type:          check.Type,
		Status:        publicCheckStatus(check.Status),
		InMaintenance: inMaintenance,
	}, check.Status, nil
}

// getGroupInfo builds the live block for a GROUP resource: the group's name,
// its status rolled up from its enabled members via models.RollupGroupStatus
// (the shared helper from spec 2026-08-01-01), and a maintenance flag that is
// true when a window targets the group OR any member check.
//
// Type is deliberately left empty and no member name, type or count is ever
// included — the whole point of a group component is hiding internal topology.
func (s *Service) getGroupInfo(
	ctx context.Context, orgUID, groupUID string, groupCounts map[string]map[models.CheckStatus]int,
) (*ResourceCheckInfo, models.CheckStatus, error) {
	group, err := s.db.GetCheckGroup(ctx, orgUID, groupUID)
	if err != nil {
		return nil, models.CheckStatusCreated, err
	}

	if group == nil {
		return nil, models.CheckStatusCreated, ErrCheckGroupNotFound
	}

	inMaintenance := false

	if windows, errMW := s.db.ListMaintenanceWindowsForCheckGroup(ctx, groupUID); errMW == nil {
		inMaintenance = anyWindowActive(windows)
	}

	name := group.Name
	rolled := models.RollupGroupStatus(groupCounts[groupUID])

	return &ResourceCheckInfo{
		Name:          &name,
		Type:          "",
		Status:        publicCheckStatus(rolled),
		InMaintenance: inMaintenance,
	}, rolled, nil
}

// anyWindowActive reports whether any of the windows is active right now.
func anyWindowActive(windows []*models.MaintenanceWindow) bool {
	now := time.Now()
	for _, w := range windows {
		if models.IsActiveAt(w, now) {
			return true
		}
	}

	return false
}

func convertPageToResponse(page *models.StatusPage) StatusPageResponse {
	thresholdUp, thresholdDegraded := page.Settings.EffectiveThresholds()

	return StatusPageResponse{
		UID:                     page.UID,
		Name:                    page.Name,
		Slug:                    page.Slug,
		Description:             page.Description,
		Visibility:              page.Visibility,
		IsDefault:               page.IsDefault,
		Enabled:                 page.Enabled,
		ShowAvailability:        page.ShowAvailability,
		ShowResponseTime:        page.ShowResponseTime,
		HistoryDays:             page.HistoryDays,
		HistoryPeriod:           string(pagePeriod(page)),
		Language:                page.Language,
		AutoPublish:             page.AutoPublish,
		AutoPublishDelaySeconds: page.AutoPublishDelaySeconds,
		AutoResolve:             page.AutoResolve,
		CustomCSS:               page.CustomCSS,
		HideBranding:            page.Settings.HideBranding(),
		HasPassword:             page.PasswordHash != nil && *page.PasswordHash != "",
		HasKioskToken:           page.KioskTokenHash != nil && *page.KioskTokenHash != "",
		LogoURL:                 statuspageassets.PublicURL(page.Settings.LogoFileUID()),
		FaviconURL:              statuspageassets.PublicURL(page.Settings.FaviconFileUID()),
		CreatedAt:               &page.CreatedAt,
		Settings:                convertSettingsToResponse(page.Settings),
		AvailabilityThresholds: AvailabilityThresholdsResponse{
			ThresholdUp:       thresholdUp,
			ThresholdDegraded: thresholdDegraded,
		},
	}
}

// convertSettingsToResponse mirrors the storage shape: an unset section
// (nil) round-trips as nil, not as zero-valued numbers, so the admin UI can
// tell "no override" from "override set to 0".
func convertSettingsToResponse(settings models.StatusPageSettings) *SettingsResponse {
	if settings.Availability == nil {
		return &SettingsResponse{}
	}

	return &SettingsResponse{
		Availability: &AvailabilitySettingsResponse{
			ThresholdUp:       settings.Availability.ThresholdUp,
			ThresholdDegraded: settings.Availability.ThresholdDegraded,
		},
	}
}

func convertSectionToResponse(section *models.StatusPageSection) StatusPageSectionResponse {
	return StatusPageSectionResponse{
		UID:       section.UID,
		Name:      section.Name,
		Slug:      section.Slug,
		Position:  section.Position,
		CreatedAt: &section.CreatedAt,
	}
}

func convertResourceToResponse(resource *models.StatusPageResource) StatusPageResourceResponse {
	return StatusPageResourceResponse{
		UID:           resource.UID,
		CheckUID:      resource.CheckUID,
		CheckGroupUID: resource.CheckGroupUID,
		PublicName:    resource.PublicName,
		Explanation:   resource.Explanation,
		AutoPublish:   resource.AutoPublish,
		Position:      resource.Position,
		CreatedAt:     &resource.CreatedAt,
	}
}

// derefOrEmpty returns the pointed-to string, or "" for a nil pointer.
func derefOrEmpty(v *string) string {
	if v == nil {
		return ""
	}

	return *v
}
