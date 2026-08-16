// Package agents provides the org-scoped admin API for private locations
// (org-private regions) and their deported check agents: creating/deleting
// private regions, minting one-shot enrollment tokens, and listing/revoking
// agents.
package agents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// DefaultEnrollmentTokenTTL is how long a minted enrollment token stays valid.
const DefaultEnrollmentTokenTTL = 24 * time.Hour

// maxEnrollmentTokenTTL bounds an operator-supplied TTL.
const maxEnrollmentTokenTTL = 30 * 24 * time.Hour

// Service errors.
var (
	ErrOrgNotFound      = errors.New("organization not found")
	ErrRegionNotFound   = errors.New("private region not found")
	ErrRegionExists     = errors.New("a private region with this slug already exists")
	ErrRegionHasAgents  = errors.New("private region still has enrolled agents; revoke them first")
	ErrAgentNotFound    = errors.New("agent not found")
	ErrInvalidExpiresIn = errors.New("expiresIn must be a positive duration no greater than 30 days")
)

// Service provides business logic for the private-locations admin API.
type Service struct {
	db      db.Service
	regions *regions.Service
	// creds is the v1 symmetric credential service, used by ResealRegion to
	// decrypt mixed-mode checks so they can be re-sealed to the region's
	// current agent set. Always non-nil; Enabled() reports availability.
	creds credentials.Service
	// ent gates MintEnrollmentToken against MaxDeportedAgents. Optional (nil
	// disables the guard) so tests that don't care about entitlements can
	// omit it, mirroring the optionality of agentws.Handler.entitlements.
	ent *entitlements.Service
}

// NewService creates a new agents admin service.
func NewService(dbService db.Service, creds credentials.Service, entSvc *entitlements.Service) *Service {
	return &Service{
		db:      dbService,
		regions: regions.NewService(dbService),
		creds:   creds,
		ent:     entSvc,
	}
}

// PrivateRegionResponse is a private region as returned by the API. `Region` is
// the namespaced string actually stored on checks/jobs — org-relative
// (`@<slug>`), because the org is already implied by the `/orgs/:org/…` path
// and by every row's organization_uid; `Slug` is the raw slug the operator
// typed.
type PrivateRegionResponse struct {
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Emoji string `json:"emoji"`
	// Region is the fully-qualified stored region string.
	Region string `json:"region"`
	// AgentCount is how many active agents currently serve this region.
	AgentCount int `json:"agentCount"`
	// Capabilities reports what this location's LIVE agents say they can do —
	// today `ipv6`, three-state ("yes" / "no" / "unknown"). This page is where
	// it matters most: a private location is the one case where the user can
	// actually FIX a missing family, by enabling IPv6 on the host they own
	// (spec 2026-08-15-11).
	Capabilities map[string]string `json:"capabilities,omitempty"`
}

// ListPrivateRegionsResponse wraps the private-region list.
type ListPrivateRegionsResponse struct {
	Data []PrivateRegionResponse `json:"data"`
}

// CreatePrivateRegionRequest is the body for creating a private region.
type CreatePrivateRegionRequest struct {
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Emoji string `json:"emoji"`
}

// resolveOrg loads an org by slug.
func (s *Service) resolveOrg(ctx context.Context, orgSlug string) (*models.Organization, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrOrgNotFound, orgSlug)
	}

	return org, nil
}

// ListPrivateRegions returns an org's private regions with their active agent counts.
func (s *Service) ListPrivateRegions(ctx context.Context, orgSlug string) (*ListPrivateRegionsResponse, error) {
	org, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	defs, err := s.regions.GetOrgCustomRegions(ctx, org.UID)
	if err != nil {
		return nil, err
	}

	if capErr := s.regions.AnnotatePrivateCapabilities(ctx, org.UID, defs); capErr != nil {
		return nil, capErr
	}

	data := make([]PrivateRegionResponse, 0, len(defs))

	for i := range defs {
		full := regions.PrivateRegionSlug(defs[i].Slug)

		active, listErr := s.db.ListActiveAgentsByRegion(ctx, org.UID, full)
		if listErr != nil {
			return nil, listErr
		}

		data = append(data, PrivateRegionResponse{
			Slug:         defs[i].Slug,
			Name:         defs[i].Name,
			Emoji:        defs[i].Emoji,
			Region:       full,
			AgentCount:   len(active),
			Capabilities: defs[i].Capabilities,
		})
	}

	return &ListPrivateRegionsResponse{Data: data}, nil
}

// CreatePrivateRegion adds a private region to an org.
func (s *Service) CreatePrivateRegion(
	ctx context.Context, orgSlug string, req *CreatePrivateRegionRequest,
) (*PrivateRegionResponse, error) {
	org, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	if slugErr := regions.ValidatePrivateRegionSlug(req.Slug); slugErr != nil {
		return nil, slugErr
	}

	defs, err := s.regions.GetOrgCustomRegions(ctx, org.UID)
	if err != nil {
		return nil, err
	}

	for i := range defs {
		if defs[i].Slug == req.Slug {
			return nil, fmt.Errorf("%w: %s", ErrRegionExists, req.Slug)
		}
	}

	name := req.Name
	if name == "" {
		name = req.Slug
	}

	emoji := req.Emoji
	if emoji == "" {
		emoji = "🏠"
	}

	defs = append(defs, regions.RegionDefinition{Slug: req.Slug, Name: name, Emoji: emoji})

	if err := s.regions.SetOrgCustomRegions(ctx, org.UID, defs); err != nil {
		return nil, err
	}

	return &PrivateRegionResponse{
		Slug:   req.Slug,
		Name:   name,
		Emoji:  emoji,
		Region: regions.PrivateRegionSlug(req.Slug),
	}, nil
}

// DeletePrivateRegion removes a private region. Refuses while agents are still
// enrolled in it — deleting the region would orphan them (and any sealed
// credentials addressed to their keys).
func (s *Service) DeletePrivateRegion(ctx context.Context, orgSlug, slug string) error {
	org, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return err
	}

	defs, err := s.regions.GetOrgCustomRegions(ctx, org.UID)
	if err != nil {
		return err
	}

	kept := make([]regions.RegionDefinition, 0, len(defs))
	found := false

	for i := range defs {
		if defs[i].Slug == slug {
			found = true

			continue
		}

		kept = append(kept, defs[i])
	}

	if !found {
		return fmt.Errorf("%w: %s", ErrRegionNotFound, slug)
	}

	full := regions.PrivateRegionSlug(slug)

	active, err := s.db.ListActiveAgentsByRegion(ctx, org.UID, full)
	if err != nil {
		return err
	}

	if len(active) > 0 {
		return fmt.Errorf("%w: %d active", ErrRegionHasAgents, len(active))
	}

	return s.regions.SetOrgCustomRegions(ctx, org.UID, kept)
}

// MintEnrollmentTokenRequest is the body for minting an enrollment token.
type MintEnrollmentTokenRequest struct {
	// RegionSlug is the raw private-region slug the agent will be bound to.
	RegionSlug string `json:"regionSlug"`
	// ExpiresIn is an optional Go duration string (default 24h, max 30d).
	ExpiresIn string `json:"expiresIn,omitempty"`
}

// MintEnrollmentTokenResponse carries the freshly-minted token. The `Token`
// field is the ONLY time the secret is ever readable — the server stores just
// its SHA-256 hash.
type MintEnrollmentTokenResponse struct {
	UID       string    `json:"uid"`
	Token     string    `json:"token"`
	Region    string    `json:"region"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// MintEnrollmentToken creates a one-shot enrollment token bound to (org, region).
func (s *Service) MintEnrollmentToken(
	ctx context.Context, orgSlug string, req *MintEnrollmentTokenRequest, createdByUserUID *string,
) (*MintEnrollmentTokenResponse, error) {
	org, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	// Enforce the MaxDeportedAgents quota before minting — early UX so the
	// dashboard can surface the upgrade prompt before the operator ever starts
	// a container. This is not the correctness point: a token minted under
	// the cap could still over-enroll if the cap drops or another token is
	// consumed first, which is why agentws' awaitEnroll checks again at
	// enrollment time.
	if s.ent != nil {
		if quotaErr := s.ent.AgentCreateAllowed(ctx, org.UID); quotaErr != nil {
			return nil, quotaErr
		}
	}

	defs, err := s.regions.GetOrgCustomRegions(ctx, org.UID)
	if err != nil {
		return nil, err
	}

	found := false

	for i := range defs {
		if defs[i].Slug == req.RegionSlug {
			found = true

			break
		}
	}

	if !found {
		return nil, fmt.Errorf("%w: %s", ErrRegionNotFound, req.RegionSlug)
	}

	ttl, err := resolveTTL(req.ExpiresIn)
	if err != nil {
		return nil, err
	}

	token, hash, err := agentcrypto.GenerateEnrollmentToken()
	if err != nil {
		return nil, err
	}

	full := regions.PrivateRegionSlug(req.RegionSlug)
	expiresAt := time.Now().Add(ttl)

	row := models.NewAgentEnrollmentToken(org.UID, full, hash, expiresAt, createdByUserUID)
	if err := s.db.CreateAgentEnrollmentToken(ctx, row); err != nil {
		return nil, err
	}

	return &MintEnrollmentTokenResponse{
		UID:       row.UID,
		Token:     token,
		Region:    full,
		ExpiresAt: expiresAt,
	}, nil
}

// resolveTTL parses the optional ExpiresIn duration, applying the default and cap.
func resolveTTL(expiresIn string) (time.Duration, error) {
	if expiresIn == "" {
		return DefaultEnrollmentTokenTTL, nil
	}

	ttl, err := time.ParseDuration(expiresIn)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrInvalidExpiresIn, err)
	}

	if ttl <= 0 || ttl > maxEnrollmentTokenTTL {
		return 0, ErrInvalidExpiresIn
	}

	return ttl, nil
}

// Enrollment token statuses as reported by the list API.
const (
	// EnrollmentTokenStatusPending is a live token still waiting for an agent.
	EnrollmentTokenStatusPending = "pending"
	// EnrollmentTokenStatusUsed is a consumed token, kept listed for a viewing
	// window (db.UsedEnrollmentTokenListWindow) so the register wizard can
	// report which agent enrolled with it. It can never enroll another agent.
	EnrollmentTokenStatusUsed = "used"
)

// EnrollmentTokenResponse is a token as listed (never includes the secret).
type EnrollmentTokenResponse struct {
	UID       string    `json:"uid"`
	Region    string    `json:"region"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expiresAt"`
	CreatedAt time.Time `json:"createdAt"`
	// UsedAt/UsedByAgentUID are set once an agent consumed the token.
	UsedAt         *time.Time `json:"usedAt,omitempty"`
	UsedByAgentUID *string    `json:"usedByAgentUid,omitempty"`
}

// ListEnrollmentTokensResponse wraps the token list.
type ListEnrollmentTokensResponse struct {
	Data []EnrollmentTokenResponse `json:"data"`
}

// ListEnrollmentTokens returns an org's live (unused, unexpired) tokens plus
// recently-used ones, so a wizard polling this list can tell "my token was
// consumed by agent X" apart from "my token was canceled".
func (s *Service) ListEnrollmentTokens(ctx context.Context, orgSlug string) (*ListEnrollmentTokensResponse, error) {
	org, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.ListAgentEnrollmentTokens(ctx, org.UID)
	if err != nil {
		return nil, err
	}

	data := make([]EnrollmentTokenResponse, 0, len(rows))

	for _, row := range rows {
		status := EnrollmentTokenStatusPending
		if row.UsedAt != nil {
			status = EnrollmentTokenStatusUsed
		}

		data = append(data, EnrollmentTokenResponse{
			UID:            row.UID,
			Region:         row.Region,
			Status:         status,
			ExpiresAt:      row.ExpiresAt,
			CreatedAt:      row.CreatedAt,
			UsedAt:         row.UsedAt,
			UsedByAgentUID: row.UsedByAgentUID,
		})
	}

	return &ListEnrollmentTokensResponse{Data: data}, nil
}

// DeleteEnrollmentToken cancels a pending enrollment token.
func (s *Service) DeleteEnrollmentToken(ctx context.Context, orgSlug, uid string) error {
	org, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return err
	}

	return s.db.DeleteAgentEnrollmentToken(ctx, org.UID, uid)
}

// AgentResponse is an agent as returned by the API. Only public key material is
// exposed, and only as a short fingerprint.
type AgentResponse struct {
	UID  string `json:"uid"`
	Name string `json:"name"`
	// Kind is "org" or "system". Only populated on the fleet-wide view
	// (ListAllAgents); the org-scoped ListAgents response omits it since every
	// row there is implicitly "org".
	Kind string `json:"kind,omitempty"`
	// Org is the owning org slug, populated only on the fleet-wide view. Null
	// for system agents, which have no owning organization.
	Org         *string    `json:"org,omitempty"`
	Region      string     `json:"region"`
	Fingerprint string     `json:"fingerprint"`
	Status      string     `json:"status"`
	LastSeenAt  *time.Time `json:"lastSeenAt,omitempty"`
	EnrolledAt  time.Time  `json:"enrolledAt"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty"`
}

// ListAgentsResponse wraps the agent list.
type ListAgentsResponse struct {
	Data []AgentResponse `json:"data"`
}

// ListAgents returns an org's agents.
func (s *Service) ListAgents(ctx context.Context, orgSlug string) (*ListAgentsResponse, error) {
	org, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.ListAgents(ctx, org.UID)
	if err != nil {
		return nil, err
	}

	data := make([]AgentResponse, 0, len(rows))
	for _, row := range rows {
		data = append(data, AgentResponse{
			UID:         row.UID,
			Name:        row.Name,
			Region:      row.Region,
			Fingerprint: row.Fingerprint,
			Status:      row.Status,
			LastSeenAt:  row.LastSeenAt,
			EnrolledAt:  row.EnrolledAt,
			RevokedAt:   row.RevokedAt,
		})
	}

	return &ListAgentsResponse{Data: data}, nil
}

// ListAllAgents returns every non-deleted agent across all organizations, both
// org and system kind, for the fleet-wide superadmin view. Org agents carry
// their owning org's slug; system agents carry a nil org.
func (s *Service) ListAllAgents(ctx context.Context) (*ListAgentsResponse, error) {
	rows, err := s.db.ListAllAgents(ctx)
	if err != nil {
		return nil, err
	}

	orgs, err := s.db.ListOrganizations(ctx)
	if err != nil {
		return nil, err
	}

	slugByUID := make(map[string]string, len(orgs))
	for _, org := range orgs {
		slugByUID[org.UID] = org.Slug
	}

	data := make([]AgentResponse, 0, len(rows))
	for _, row := range rows {
		var orgSlug *string
		if row.OrganizationUID != nil {
			if slug, ok := slugByUID[*row.OrganizationUID]; ok {
				orgSlug = &slug
			}
		}

		data = append(data, AgentResponse{
			UID:         row.UID,
			Name:        row.Name,
			Kind:        row.Kind,
			Org:         orgSlug,
			Region:      row.Region,
			Fingerprint: row.Fingerprint,
			Status:      row.Status,
			LastSeenAt:  row.LastSeenAt,
			EnrolledAt:  row.EnrolledAt,
			RevokedAt:   row.RevokedAt,
		})
	}

	return &ListAgentsResponse{Data: data}, nil
}

// RevokeAgent revokes an agent. A revoked agent can no longer authenticate (any
// live connection is closed on its next frame) and is excluded from future
// credential seals. Mixed-mode checks of its region are re-sealed immediately
// (excluding it); sealed-only checks surface as needs-re-seal. Honest caveat
// for the docs: a revoked agent already saw the credentials sealed to it —
// rotate them.
func (s *Service) RevokeAgent(ctx context.Context, orgSlug, uid string) error {
	org, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return err
	}

	// OrgUID() is "" for a platform-operated (system) agent, which therefore
	// never matches: system agents are not org-admin material and are managed
	// only by the platform (seeded tokens + the agent_gc job).
	agent, err := s.db.GetAgent(ctx, uid)
	if err != nil || agent.OrgUID() != org.UID {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, uid)
	}

	if err := s.db.RevokeAgent(ctx, org.UID, uid); err != nil {
		return err
	}

	s.ResealRegion(ctx, org.UID, agent.Region)

	return nil
}

// ResealRegion re-seals the region's mixed-mode checks (those the server can
// still decrypt via the org DEK) to the region's CURRENT active agent set.
// Called after agent membership changes: a new enrollment (so the fresh agent
// can decrypt existing credentials without re-entry) and a revocation (so the
// revoked key is dropped from future blobs). Sealed-only checks cannot be
// re-sealed server-side — they surface as needs-re-seal until the credentials
// are re-saved. Best-effort by design: a partial failure is logged, never
// blocks the membership change itself.
func (s *Service) ResealRegion(ctx context.Context, orgUID, region string) {
	if !s.creds.Enabled() {
		return // no org DEK — nothing mixed-mode to decrypt
	}

	checks, _, err := s.db.ListChecks(ctx, orgUID, &models.ListChecksFilter{})
	if err != nil {
		slog.WarnContext(ctx, "reseal: failed to list checks", "error", err, "region", region)

		return
	}

	for _, check := range checks {
		if !regionTargeted(check.Regions, region) {
			continue
		}

		// Only mixed-mode rows are re-sealable: the server needs the v1
		// envelope to recover the plaintext.
		if check.ConfigPrivate == nil || *check.ConfigPrivate == "" {
			continue
		}

		s.resealCheck(ctx, check)
	}
}

// resealCheck re-seals one mixed-mode check to the current active agents of
// ALL its private regions, updating the check row and its job rows.
func (s *Service) resealCheck(ctx context.Context, check *models.Check) {
	private, err := s.creds.DecryptForOrg(ctx, check.OrganizationUID, *check.ConfigPrivate)
	if err != nil {
		slog.WarnContext(ctx, "reseal: cannot decrypt check", "checkUid", check.UID, "error", err)

		return
	}

	recipients := make([]string, 0, 4)
	seen := make(map[string]struct{})

	for _, region := range check.Regions {
		if !regions.IsPrivateRegion(region) {
			continue
		}

		agents, listErr := s.db.ListActiveAgentsByRegion(ctx, check.OrganizationUID, region)
		if listErr != nil {
			slog.WarnContext(ctx, "reseal: cannot list agents", "region", region, "error", listErr)

			return
		}

		for _, agent := range agents {
			if _, dup := seen[agent.X25519PublicKey]; dup {
				continue
			}

			seen[agent.X25519PublicKey] = struct{}{}
			recipients = append(recipients, agent.X25519PublicKey)
		}
	}

	update := &models.CheckUpdate{}

	if len(recipients) == 0 {
		// No active agents left — drop the sealed blob entirely (nothing can
		// decrypt it, and keeping stale recipients around is pure liability).
		update.ClearConfigSealed = true
	} else {
		sealed, sealErr := credentials.SealForRecipients(recipients, private)
		if sealErr != nil {
			slog.WarnContext(ctx, "reseal: sealing failed", "checkUid", check.UID, "error", sealErr)

			return
		}

		update.ConfigSealed = &sealed
	}

	if err := s.db.UpdateCheck(ctx, check.UID, update); err != nil {
		slog.WarnContext(ctx, "reseal: check update failed", "checkUid", check.UID, "error", err)

		return
	}

	// Keep the dispatch rows in step immediately (reconcile would otherwise
	// only sync them on the next check write).
	if _, err := s.db.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("config_sealed = ?", update.ConfigSealed).
		Set("updated_at = ?", time.Now()).
		Where("check_uid = ?", check.UID).
		Exec(ctx); err != nil {
		slog.WarnContext(ctx, "reseal: job sync failed", "checkUid", check.UID, "error", err)
	}
}

// regionTargeted reports whether a region set contains the given region.
func regionTargeted(regionSlugs []string, region string) bool {
	for _, r := range regionSlugs {
		if r == region {
			return true
		}
	}

	return false
}
