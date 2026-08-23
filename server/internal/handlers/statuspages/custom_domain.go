package statuspages

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/customdomain"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/domainverify"
	"github.com/fclairamb/solidping/server/internal/statuspagelock"
)

// Custom-domain tuning constants.
const (
	// customDomainVerifyPerMinute caps synchronous verify-now calls per org.
	customDomainVerifyPerMinute = 10
	// customDomainTokenBytes is the entropy of the CNAME token. 8 bytes → 13
	// base32 characters; with customDomainTokenPrefix the token is 15 chars,
	// well inside the 63-char DNS label cap it has to live in as the leading
	// label of "<token>.cname.<target>" in token mode.
	customDomainTokenBytes = 8
	// customDomainTokenPrefix guarantees the token starts with a letter. Some
	// DNS providers reject a label beginning with a digit, and base32's
	// alphabet includes 2-7.
	customDomainTokenPrefix = "sp"
)

// Custom-domain errors. Mapped to HTTP by the handler.
var (
	// ErrCustomDomainInvalid is returned when the hostname fails normalization.
	ErrCustomDomainInvalid = errors.New("invalid custom domain")
	// ErrCustomDomainTaken is returned when another live page already holds the
	// domain (409). The global partial unique index is the hard guarantee.
	ErrCustomDomainTaken = errors.New("custom domain already in use")
	// ErrCustomDomainSelfShadow is returned when the domain equals (or is a
	// subdomain of) one of the instance's own hosts.
	ErrCustomDomainSelfShadow = errors.New("custom domain shadows an instance host")
	// ErrCustomDomainNotSet is returned when a verify is requested for a page
	// that has no custom domain.
	ErrCustomDomainNotSet = errors.New("no custom domain configured")
	// ErrCustomDomainRateLimited is returned when the per-org verify rate limit
	// is exceeded.
	ErrCustomDomainRateLimited = errors.New("custom domain verification rate limit exceeded")
)

// generateCustomDomainToken returns an unguessable, DNS-label-safe token
// (crypto/rand, lowercase base32, letter-leading). In token mode the token IS
// the leading label of the CNAME target the customer publishes, so it must be
// short and label-legal; in shared mode it is unused but still generated so a
// deployment can flip modes without re-provisioning pages.
//
// Migration note: pre-v0.8.0 tokens are 43-char base64url and may contain
// characters illegal in a DNS label. They are never rewritten in place — a page
// picks up a modern token the next time its domain is set (see setCustomDomain),
// and domainverify.TokenHost returns "" for a token it cannot use, which fails
// closed instead of publishing a malformed record.
func generateCustomDomainToken() (string, error) {
	buf := make([]byte, customDomainTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate custom domain token: %w", err)
	}

	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)

	return customDomainTokenPrefix + strings.ToLower(encoded), nil
}

// cnameTarget resolves the installation's CNAME target from config.
func (s *Service) cnameTarget() string {
	if s.cfg == nil {
		return ""
	}

	return s.cfg.CustomDomainCNAMETarget()
}

// cnameMode resolves the configured CNAME verification mode, defaulting to
// shared when no config is attached (the MCP handler).
func (s *Service) cnameMode() domainverify.Mode {
	if s.cfg == nil {
		return domainverify.ModeShared
	}

	return s.cfg.CustomDomainCNAMEMode()
}

// reservedHosts are the instance's own hostnames a custom domain must not
// shadow: the base-url host, the docs host, and the CNAME target.
func (s *Service) reservedHosts() []string {
	if s.cfg == nil {
		return nil
	}

	hosts := make([]string, 0, 3)

	if target := s.cnameTarget(); target != "" {
		hosts = append(hosts, target)
	}

	if docs := strings.ToLower(strings.TrimSpace(s.cfg.Server.DocsHost)); docs != "" {
		hosts = append(hosts, docs)
	}

	// The base-url host is distinct from the CNAME target when an explicit
	// target is configured, so derive it independently.
	if parsed, err := url.Parse(s.cfg.Server.BaseURL); err == nil {
		if host := strings.ToLower(parsed.Hostname()); host != "" {
			hosts = append(hosts, host)
		}
	}

	return hosts
}

// isSelfShadowing reports whether a (normalized) domain equals or is a
// subdomain of any reserved instance host.
func (s *Service) isSelfShadowing(domain string) bool {
	for _, host := range s.reservedHosts() {
		if domain == host || strings.HasSuffix(domain, "."+host) {
			return true
		}
	}

	return false
}

// enrichCustomDomain populates the authenticated-only custom-domain response
// fields. It is called ONLY from the authenticated org endpoints — never from
// the public view — so the domain and its challenge token never leak publicly.
func (s *Service) enrichCustomDomain(resp *StatusPageResponse, page *models.StatusPage) {
	if page.CustomDomain == nil {
		return
	}

	resp.CustomDomain = page.CustomDomain

	if page.CustomDomainVerifiedAt != nil {
		resp.CustomDomainStatus = "verified"
	} else {
		resp.CustomDomainStatus = "unverified"
	}

	// The lifecycle state is the richer answer: "verified" hides the difference
	// between a healthy domain and one that is still serving only because it is
	// inside its grace window, and that difference is the operator's whole
	// warning that something needs fixing before the page goes dark.
	resp.CustomDomainState = customdomain.Normalize(
		customdomain.State{Lifecycle: page.CustomDomainState, VerifiedAt: page.CustomDomainVerifiedAt},
		true, page.CustomDomainCheckedAt != nil,
	).Lifecycle
	resp.CustomDomainDegradedSince = page.CustomDomainGraceSince
	resp.CustomDomainLastCheck = page.CustomDomainLastCheck

	token := ""
	if page.CustomDomainToken != nil {
		token = *page.CustomDomainToken
	}

	resp.CustomDomainRecords = domainverify.Records(*page.CustomDomain, token, s.cnameTarget(), s.cnameMode())

	// Certificate state is only meaningful once the domain is verified and
	// in-server ACME is running; without a provider the field stays empty and
	// the dashboard simply does not render the chip.
	if s.certStatus != nil && page.CustomDomainVerifiedAt != nil {
		resp.CustomDomainCertStatus = s.certStatus.CertStatus(*page.CustomDomain)
	}
}

// applyCustomDomainChange resolves the custom-domain intent from an update
// request (presence-detected) and applies it: an explicit null/"" clears, a
// non-empty value sets/changes.
func (s *Service) applyCustomDomainChange(
	ctx context.Context, orgUID string, page *models.StatusPage, req *UpdateStatusPageRequest,
) error {
	if !req.CustomDomainSet {
		return nil
	}

	raw := ""
	if req.CustomDomain != nil {
		raw = strings.TrimSpace(*req.CustomDomain)
	}

	if raw == "" {
		return s.clearCustomDomain(ctx, page)
	}

	return s.setCustomDomain(ctx, orgUID, page, raw)
}

// setCustomDomain normalizes, validates, gates, and writes a new/changed custom
// domain, generating a fresh challenge token and clearing any prior
// verification.
func (s *Service) setCustomDomain(
	ctx context.Context, orgUID string, page *models.StatusPage, raw string,
) error {
	normalized, err := domainverify.Normalize(raw)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCustomDomainInvalid, err)
	}

	// Unchanged domain → keep the existing token and verification state.
	if page.CustomDomain != nil && *page.CustomDomain == normalized {
		return nil
	}

	if s.isSelfShadowing(normalized) {
		return ErrCustomDomainSelfShadow
	}

	// Quota gate only when adding a brand-new domain to a page that had none;
	// swapping an existing domain does not change the org's count.
	if page.CustomDomain == nil && s.ent != nil {
		if quotaErr := s.ent.CustomDomainAllowed(ctx, orgUID); quotaErr != nil {
			return quotaErr
		}
	}

	existing, err := s.db.GetStatusPageByCustomDomain(ctx, normalized)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	if existing != nil && existing.UID != page.UID {
		return ErrCustomDomainTaken
	}

	token, err := generateCustomDomainToken()
	if err != nil {
		return err
	}

	update := &models.StatusPageCustomDomainUpdate{
		Domain:     &normalized,
		Token:      &token,
		VerifiedAt: nil,
		CheckedAt:  nil,
		Failures:   0,
		// A brand-new (or re-pointed) domain starts in `pending`: configured,
		// never verified, and NOT eligible for the sweep's automatic
		// re-promotion. Only a domain that was ours before may earn its way
		// back on its own.
		State: models.CustomDomainStatePending,
	}

	if writeErr := s.db.UpdateStatusPageCustomDomain(ctx, page.UID, update); writeErr != nil {
		if isUniqueViolation(writeErr) {
			return ErrCustomDomainTaken
		}

		return writeErr
	}

	// Moving to a different hostname leaves the previous one unmapped, so drop
	// its TLS material the same way clearing does. The unchanged-domain case
	// returned earlier, so reaching here always means the old one is gone.
	s.forget(ctx, page.CustomDomain)

	return nil
}

// clearCustomDomain removes the page's custom domain and all related state.
func (s *Service) clearCustomDomain(ctx context.Context, page *models.StatusPage) error {
	if page.CustomDomain == nil {
		return nil
	}

	if err := s.db.UpdateStatusPageCustomDomain(ctx, page.UID, &models.StatusPageCustomDomainUpdate{}); err != nil {
		return err
	}

	// Drop the certificate and private key too. The edge already refuses to
	// serve a host with no mapping, so this is not what stops the domain
	// working — it is so we do not keep a live key for a hostname that may now
	// belong to someone else.
	s.forget(ctx, page.CustomDomain)

	return nil
}

// VerifyCustomDomain runs the DNS checks synchronously and stamps the result.
// Success verifies + resets failures; failure clears verification but preserves
// the running failure count. Rate-limited per org.
func (s *Service) VerifyCustomDomain(
	ctx context.Context, orgSlug, identifier string,
) (StatusPageResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return StatusPageResponse{}, ErrOrganizationNotFound
	}

	page, err := s.db.GetStatusPageByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || page == nil {
		return StatusPageResponse{}, ErrStatusPageNotFound
	}

	if page.CustomDomain == nil {
		return StatusPageResponse{}, ErrCustomDomainNotSet
	}

	if s.verifyLimiter != nil && !s.verifyLimiter.allow(org.UID) {
		return StatusPageResponse{}, ErrCustomDomainRateLimited
	}

	token := ""
	if page.CustomDomainToken != nil {
		token = *page.CustomDomainToken
	}

	diag := s.verifier.Diagnose(ctx, *page.CustomDomain, token, s.cnameTarget(), s.cnameMode())

	now := time.Now()
	update, hardDemoted := customDomainVerifyUpdate(page, &diag, now)

	if writeErr := s.db.UpdateStatusPageCustomDomain(ctx, page.UID, update); writeErr != nil {
		return StatusPageResponse{}, writeErr
	}

	// A hard demotion reached by clicking Verify takes the page just as dark as
	// one the sweep reaches on its own, so it alerts identically. Requirement 4
	// of spec 2026-08-23-03 says "alert the operator on hard demotion" without
	// qualifying the path — and the admin who clicked is not necessarily the
	// only person who needs to know.
	if hardDemoted {
		customdomain.AlertDemoted(ctx, s.demotionAlertDeps(), page, update.Failures, diag.String())
	}

	updated, err := s.db.GetStatusPage(ctx, org.UID, page.UID)
	if err != nil {
		return StatusPageResponse{}, err
	}

	response := convertPageToResponse(updated)
	s.enrichCustomDomain(&response, updated)
	s.enrichAdminBranding(&response, s.whiteLabelAllowed(ctx, org.UID))

	return response, nil
}

// customDomainVerifyUpdate turns one synchronous verification into the columns
// to write.
//
// A PASS promotes immediately and unconditionally: this is an operator with
// dashboard access saying "this domain is mine", which is the one action the
// automatic sweep is deliberately not allowed to take on its own.
//
// A FAILURE goes through the SAME lifecycle transition the sweep uses. It used
// to clear custom_domain_verified_at outright, which meant an admin who clicked
// Verify during a DNS blip took their own live status page dark instantly —
// the sweep would only have moved it into grace. Two code paths disagreeing
// about when a page stops being served is how a status page goes dark for a
// reason nobody can reconstruct afterwards.
func customDomainVerifyUpdate(
	page *models.StatusPage, diag *domainverify.Diagnosis, now time.Time,
) (*models.StatusPageCustomDomainUpdate, bool) {
	summary := diag.String()

	update := &models.StatusPageCustomDomainUpdate{
		Domain:    page.CustomDomain,
		Token:     page.CustomDomainToken,
		CheckedAt: &now,
		LastCheck: &summary,
	}

	if diag.OK {
		update.VerifiedAt = &now
		update.Failures = 0
		update.Successes = 0
		update.State = models.CustomDomainStateActive

		return update, false
	}

	current := customdomain.Normalize(
		customdomain.State{
			Lifecycle:  page.CustomDomainState,
			VerifiedAt: page.CustomDomainVerifiedAt,
			Failures:   page.CustomDomainFailures,
			Successes:  page.CustomDomainSuccesses,
			GraceSince: page.CustomDomainGraceSince,
		},
		true, page.CustomDomainCheckedAt != nil,
	)

	outcome := customdomain.Next(current, customdomain.Observation{Now: now})

	update.VerifiedAt = outcome.VerifiedAt
	update.Failures = outcome.Failures
	update.Successes = outcome.Successes
	update.State = outcome.Lifecycle
	update.GraceSince = outcome.GraceSince

	return update, outcome.HardDemoted
}

// demotionAlertDeps builds the alert's dependencies from the service's own
// wiring. A Service with no job service (the MCP handler, most tests) still
// records the audit event — the fact is never lost just because mail is not
// wired.
func (s *Service) demotionAlertDeps() customdomain.AlertDeps {
	deps := customdomain.AlertDeps{DB: s.db, Jobs: s.jobs} //nolint:exhaustruct // logger defaults

	if s.cfg != nil {
		deps.BaseURL = s.cfg.Server.BaseURL
	}

	return deps
}

// CustomDomainServable reports whether a domain currently resolves to a
// verified, enabled, public page — the contract the edge-TLS "allowed" endpoint
// answers and the host resolver enforces.
func (s *Service) CustomDomainServable(ctx context.Context, domain string) bool {
	normalized := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if normalized == "" {
		return false
	}

	page, err := s.db.GetStatusPageByCustomDomain(ctx, normalized)
	if err != nil || page == nil {
		return false
	}

	// A password-protected page still needs a certificate on its custom
	// domain: the unlock form is served over that hostname, and refusing the
	// cert would make the page unreachable rather than protected.
	return page.CustomDomainVerifiedAt != nil && statuspagelock.Visible(page)
}

// isUniqueViolation reports whether an error looks like a unique-constraint
// violation from either SQLite or PostgreSQL (string match — the drivers wrap
// errors differently and we only need "duplicate row" vs anything else).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()

	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "duplicate key value") ||
		strings.Contains(msg, "SQLSTATE 23505")
}

// verifyRateLimiter is a per-org fixed-window counter guarding the synchronous
// verify-now endpoint. Process-local (like entitlements' token bucket);
// multi-replica deployments would need a shared store, which is a follow-up.
type verifyRateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	state  map[string]*verifyWindow
}

type verifyWindow struct {
	start time.Time
	count int
}

func newVerifyRateLimiter(limit int, window time.Duration) *verifyRateLimiter {
	return &verifyRateLimiter{
		limit:  limit,
		window: window,
		state:  make(map[string]*verifyWindow),
	}
}

// allow records an attempt for orgUID and reports whether it is within the
// window's limit.
func (l *verifyRateLimiter) allow(orgUID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	win, ok := l.state[orgUID]
	if !ok || now.Sub(win.start) >= l.window {
		l.state[orgUID] = &verifyWindow{start: now, count: 1}

		return true
	}

	if win.count >= l.limit {
		return false
	}

	win.count++

	return true
}
