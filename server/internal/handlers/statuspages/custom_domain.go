package statuspages

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/domainverify"
)

// Custom-domain tuning constants.
const (
	// customDomainVerifyPerMinute caps synchronous verify-now calls per org.
	customDomainVerifyPerMinute = 10
	// customDomainTokenBytes is the entropy of the DNS-challenge token.
	customDomainTokenBytes = 32
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

// generateCustomDomainToken returns a URL-safe, unguessable challenge token
// (crypto/rand, base64url), mirroring statussubscribers.generateToken.
func generateCustomDomainToken() (string, error) {
	buf := make([]byte, customDomainTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate custom domain token: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// cnameTarget resolves the installation's CNAME target from config.
func (s *Service) cnameTarget() string {
	if s.cfg == nil {
		return ""
	}

	return s.cfg.CustomDomainCNAMETarget()
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

	token := ""
	if page.CustomDomainToken != nil {
		token = *page.CustomDomainToken
	}

	resp.CustomDomainRecords = domainverify.Records(*page.CustomDomain, token, s.cnameTarget())
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
	}

	if writeErr := s.db.UpdateStatusPageCustomDomain(ctx, page.UID, update); writeErr != nil {
		if isUniqueViolation(writeErr) {
			return ErrCustomDomainTaken
		}

		return writeErr
	}

	return nil
}

// clearCustomDomain removes the page's custom domain and all related state.
func (s *Service) clearCustomDomain(ctx context.Context, page *models.StatusPage) error {
	if page.CustomDomain == nil {
		return nil
	}

	return s.db.UpdateStatusPageCustomDomain(ctx, page.UID, &models.StatusPageCustomDomainUpdate{})
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

	verified := s.verifier.Verify(ctx, *page.CustomDomain, token, s.cnameTarget())

	now := time.Now()
	update := &models.StatusPageCustomDomainUpdate{
		Domain:    page.CustomDomain,
		Token:     page.CustomDomainToken,
		CheckedAt: &now,
	}

	if verified {
		update.VerifiedAt = &now
		update.Failures = 0
	} else {
		update.VerifiedAt = nil
		update.Failures = page.CustomDomainFailures
	}

	if writeErr := s.db.UpdateStatusPageCustomDomain(ctx, page.UID, update); writeErr != nil {
		return StatusPageResponse{}, writeErr
	}

	updated, err := s.db.GetStatusPage(ctx, org.UID, page.UID)
	if err != nil {
		return StatusPageResponse{}, err
	}

	response := convertPageToResponse(updated)
	s.enrichCustomDomain(&response, updated)

	return response, nil
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

	return page.CustomDomainVerifiedAt != nil && page.Enabled && page.Visibility == "public"
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
