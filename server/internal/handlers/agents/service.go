// Package agents provides the org-scoped admin API for private locations
// (org-private regions) and their deported check agents: creating/deleting
// private regions, minting one-shot enrollment tokens, and listing/revoking
// agents.
package agents

import (
	"context"
	"errors"
	"fmt"
	"time"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
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
}

// NewService creates a new agents admin service.
func NewService(dbService db.Service) *Service {
	return &Service{
		db:      dbService,
		regions: regions.NewService(dbService),
	}
}

// PrivateRegionResponse is a private region as returned by the API. `Region` is
// the fully-qualified, namespaced string actually stored on checks/jobs
// (`@<org>/<slug>`); `Slug` is the raw slug the operator typed.
type PrivateRegionResponse struct {
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Emoji string `json:"emoji"`
	// Region is the fully-qualified stored region string.
	Region string `json:"region"`
	// AgentCount is how many active agents currently serve this region.
	AgentCount int `json:"agentCount"`
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

	data := make([]PrivateRegionResponse, 0, len(defs))

	for i := range defs {
		full := regions.PrivateRegionSlug(orgSlug, defs[i].Slug)

		active, listErr := s.db.ListActiveAgentsByRegion(ctx, org.UID, full)
		if listErr != nil {
			return nil, listErr
		}

		data = append(data, PrivateRegionResponse{
			Slug:       defs[i].Slug,
			Name:       defs[i].Name,
			Emoji:      defs[i].Emoji,
			Region:     full,
			AgentCount: len(active),
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
		Region: regions.PrivateRegionSlug(orgSlug, req.Slug),
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

	full := regions.PrivateRegionSlug(orgSlug, slug)

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

	full := regions.PrivateRegionSlug(orgSlug, req.RegionSlug)
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

// EnrollmentTokenResponse is a live token as listed (never includes the secret).
type EnrollmentTokenResponse struct {
	UID       string    `json:"uid"`
	Region    string    `json:"region"`
	ExpiresAt time.Time `json:"expiresAt"`
	CreatedAt time.Time `json:"createdAt"`
}

// ListEnrollmentTokensResponse wraps the live-token list.
type ListEnrollmentTokensResponse struct {
	Data []EnrollmentTokenResponse `json:"data"`
}

// ListEnrollmentTokens returns an org's live (unused, unexpired) tokens.
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
		data = append(data, EnrollmentTokenResponse{
			UID:       row.UID,
			Region:    row.Region,
			ExpiresAt: row.ExpiresAt,
			CreatedAt: row.CreatedAt,
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
	UID         string     `json:"uid"`
	Name        string     `json:"name"`
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

// RevokeAgent revokes an agent. A revoked agent can no longer authenticate (any
// live connection is closed on its next frame) and is excluded from future
// credential seals. Honest caveat for the docs: a revoked agent already saw the
// credentials sealed to it — rotate them.
func (s *Service) RevokeAgent(ctx context.Context, orgSlug, uid string) error {
	org, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return err
	}

	agent, err := s.db.GetAgent(ctx, uid)
	if err != nil || agent.OrganizationUID != org.UID {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, uid)
	}

	return s.db.RevokeAgent(ctx, org.UID, uid)
}
