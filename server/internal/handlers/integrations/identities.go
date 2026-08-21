package integrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/slack"
)

// Identity-mapping errors.
var (
	// ErrIdentitiesUnsupportedType is returned when identity mapping is asked
	// of an integration type that has no identity concept. Slack and bot-mode
	// Discord are supported; Teams is explicitly out of scope
	// (spec 2026-08-12-03).
	ErrIdentitiesUnsupportedType = errors.New(
		"identity mapping is only available for Slack and Discord bot integrations")
	// ErrSlackNotConnected is returned when the Slack integration carries no
	// bot token, so no workspace lookup is possible.
	ErrSlackNotConnected = errors.New("slack integration is not connected — install the Slack app")
	// ErrIdentityExternalIDRequired is returned when a manual override omits
	// the provider-side identifier.
	ErrIdentityExternalIDRequired = errors.New("externalId is required")
	// ErrIdentityMemberNotFound is returned when the target user is not a
	// member of the integration's organization.
	ErrIdentityMemberNotFound = errors.New("user is not a member of this organization")
	// ErrIdentityAlreadyClaimed is returned when the requested external id is
	// already mapped to a different member on this integration. Mapping one
	// Slack account to two people would make mentions lie about who is on call.
	ErrIdentityAlreadyClaimed = errors.New("that workspace user is already mapped to another member")
)

// Identity mapping statuses reported per member.
const (
	// IdentityStatusMatched means the member has an identity on this integration.
	IdentityStatusMatched = "matched"
	// IdentityStatusNotFound means the member's email resolved to nobody in the
	// workspace (or was never synced).
	IdentityStatusNotFound = "notFound"
	// IdentityStatusAmbiguous means the auto-match resolved to a workspace user
	// that another member already owns. Never guessed — surfaced for an admin
	// to settle with a manual override.
	IdentityStatusAmbiguous = "ambiguous"
)

// MemberIdentityResponse is the per-member mapping status returned by the
// identity endpoints. Only identity data travels — never a contact value.
type MemberIdentityResponse struct {
	UserUID     string `json:"userUid"`
	Email       string `json:"email"`
	Name        string `json:"name,omitempty"`
	Status      string `json:"status"`
	ExternalID  string `json:"externalId,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Source      string `json:"source,omitempty"`
}

// ListIdentitiesResponse wraps the per-member mapping list.
type ListIdentitiesResponse struct {
	Data []*MemberIdentityResponse `json:"data"`
}

// SyncIdentitiesResponse reports the post-sync state plus the bucket counts the
// dashboard summarizes ("12 matched, 3 not found").
type SyncIdentitiesResponse struct {
	Data           []*MemberIdentityResponse `json:"data"`
	MatchedCount   int                       `json:"matchedCount"`
	NotFoundCount  int                       `json:"notFoundCount"`
	AmbiguousCount int                       `json:"ambiguousCount"`
}

// SetIdentityRequest is the body of the manual-override PUT.
type SetIdentityRequest struct {
	ExternalID  string `json:"externalId"`
	DisplayName string `json:"displayName,omitempty"`
}

// SlackIdentityLookup is the slice of the Slack API the identity auto-match
// needs. Narrow on purpose so tests can supply a fake without a Slack server.
type SlackIdentityLookup interface {
	LookupUserByEmail(ctx context.Context, email string) (*slack.SlackUser, bool, error)
}

// SlackIdentityClientFactory builds a lookup client from a bot token.
type SlackIdentityClientFactory func(token string) SlackIdentityLookup

// defaultSlackIdentityClient is the production factory.
func defaultSlackIdentityClient(token string) SlackIdentityLookup {
	return slack.NewClient(token)
}

// SetSlackIdentityClient overrides the Slack lookup factory for this service
// instance. Per-instance rather than package-level so parallel tests can never
// race on a shared seam.
func (s *Service) SetSlackIdentityClient(factory SlackIdentityClientFactory) {
	s.slackIdentityClient = factory
}

// identityContext bundles what every identity operation needs: the resolved
// org, the Slack integration, and its members.
type identityContext struct {
	org     *models.Organization
	conn    *models.Integration
	members []*models.OrganizationMember
}

// loadIdentityContext resolves the org + integration for an identity call and
// validates that identity mapping applies to it at all.
func (s *Service) loadIdentityContext(
	ctx context.Context, orgSlug, integrationUID string,
) (*identityContext, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	conn, err := s.db.GetChannel(ctx, integrationUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrConnectionNotFound
		}

		return nil, err
	}

	if conn == nil || conn.OrganizationUID != org.UID || conn.DeletedAt != nil {
		return nil, ErrConnectionNotFound
	}

	if !supportsIdentities(conn) {
		return nil, ErrIdentitiesUnsupportedType
	}

	members, err := s.db.ListMembersByOrg(ctx, org.UID)
	if err != nil {
		return nil, fmt.Errorf("list org members: %w", err)
	}

	return &identityContext{org: org, conn: conn, members: members}, nil
}

// supportsIdentities reports whether identity mapping applies to this
// integration.
//
// A LEGACY webhook-mode Discord integration is excluded on purpose: a webhook
// post carries no bot identity and cannot render a `<@id>` ping, so a mapping
// there would be a setting that quietly does nothing.
func supportsIdentities(conn *models.Integration) bool {
	if conn.Type == models.ConnectionTypeSlack {
		return true
	}

	if conn.Type == models.ConnectionTypeDiscord {
		settings, err := models.DiscordSettingsFromJSONMap(conn.Settings)

		return err == nil && settings.UsesBot()
	}

	return false
}

// identityHit is one resolved provider-side account, in the shape the sync
// needs regardless of where it came from — a Slack workspace lookup or a
// Discord sign-in already on file.
type identityHit struct {
	ID          string
	DisplayName string
}

// memberUser returns the live user behind a membership row, or nil when the
// relation is missing or the user is soft-deleted.
func memberUser(member *models.OrganizationMember) *models.User {
	if member == nil || member.User == nil || member.User.DeletedAt != nil {
		return nil
	}

	return member.User
}

// identitiesByUser indexes an integration's identity rows by user UID.
func identitiesByUser(rows []*models.UserIntegrationIdentity) map[string]*models.UserIntegrationIdentity {
	byUser := make(map[string]*models.UserIntegrationIdentity, len(rows))
	for _, row := range rows {
		byUser[row.UserUID] = row
	}

	return byUser
}

// sortIdentityResponses orders by display label so both the admin table and the
// rendered mention list are stable and testable.
func sortIdentityResponses(entries []*MemberIdentityResponse) {
	sort.SliceStable(entries, func(i, j int) bool {
		left := entries[i].Name
		if left == "" {
			left = entries[i].Email
		}

		right := entries[j].Name
		if right == "" {
			right = entries[j].Email
		}

		if !strings.EqualFold(left, right) {
			return strings.ToLower(left) < strings.ToLower(right)
		}

		return entries[i].UserUID < entries[j].UserUID
	})
}

// buildIdentityEntry renders one member's current mapping state.
func buildIdentityEntry(
	user *models.User, identity *models.UserIntegrationIdentity,
) *MemberIdentityResponse {
	entry := &MemberIdentityResponse{
		UserUID: user.UID,
		Email:   user.Email,
		Name:    user.Name,
		Status:  IdentityStatusNotFound,
	}

	if identity != nil {
		entry.Status = IdentityStatusMatched
		entry.ExternalID = identity.ExternalID
		entry.DisplayName = identity.DisplayName
		entry.Source = identity.Source
	}

	return entry
}

// ListIdentities returns the mapping status of every org member on one
// integration. Purely a read — it never calls out to Slack, so the dashboard
// can render the panel without spending an API call or a workspace round-trip.
func (s *Service) ListIdentities(
	ctx context.Context, orgSlug, integrationUID string,
) (*ListIdentitiesResponse, error) {
	ictx, err := s.loadIdentityContext(ctx, orgSlug, integrationUID)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.ListUserIntegrationIdentities(ctx, ictx.conn.UID)
	if err != nil {
		return nil, fmt.Errorf("list identities: %w", err)
	}

	byUser := identitiesByUser(rows)
	entries := make([]*MemberIdentityResponse, 0, len(ictx.members))

	for _, member := range ictx.members {
		user := memberUser(member)
		if user == nil {
			continue
		}

		entries = append(entries, buildIdentityEntry(user, byUser[user.UID]))
	}

	sortIdentityResponses(entries)

	return &ListIdentitiesResponse{Data: entries}, nil
}

// slackAccessToken decrypts the integration settings and returns the bot token.
func (s *Service) slackAccessToken(ctx context.Context, conn *models.Integration) (string, error) {
	merged, err := s.loadDecryptedSettings(ctx, conn)
	if err != nil {
		return "", err
	}

	settings, err := models.SlackSettingsFromJSONMap(models.JSONMap(merged))
	if err != nil {
		return "", fmt.Errorf("parse slack settings: %w", err)
	}

	if settings.AccessToken == "" {
		return "", ErrSlackNotConnected
	}

	return settings.AccessToken, nil
}

// SyncIdentities re-runs the email auto-match against the Slack workspace.
//
// Rules, in order:
//   - A `manual` row is never touched — an admin's explicit choice outranks
//     anything an email match can infer.
//   - A workspace hit whose user id is already owned by ANOTHER member is
//     reported ambiguous and written nowhere. Guessing here would make an
//     alert ping the wrong human.
//   - A member whose email resolves to nobody keeps whatever identity they
//     already had (a transient Slack answer must not wipe a working mapping)
//     and is only reported "not found" when they had none.
func (s *Service) SyncIdentities(
	ctx context.Context, orgSlug, integrationUID string,
) (*SyncIdentitiesResponse, error) {
	ictx, err := s.loadIdentityContext(ctx, orgSlug, integrationUID)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.ListUserIntegrationIdentities(ctx, ictx.conn.UID)
	if err != nil {
		return nil, fmt.Errorf("list identities: %w", err)
	}

	byUser := identitiesByUser(rows)
	owners := externalIDOwners(rows)

	// Phase 1 — resolve every member, writing nothing yet. Resolving first
	// makes the outcome independent of member iteration order: two people who
	// collide on one provider account must BOTH be reported ambiguous, not
	// "whoever the query returned first wins".
	resolved, err := s.resolveIdentities(ctx, ictx, byUser)
	if err != nil {
		return nil, err
	}

	claimCount := make(map[string]int, len(resolved))
	for _, hit := range resolved {
		claimCount[hit.ID]++
	}

	// Phase 2 — persist the unambiguous hits.
	entries := make([]*MemberIdentityResponse, 0, len(ictx.members))

	for _, member := range ictx.members {
		user := memberUser(member)
		if user == nil {
			continue
		}

		entry, syncErr := s.applyResolution(
			ctx, ictx.conn, user, byUser[user.UID], resolved[user.UID], owners, claimCount)
		if syncErr != nil {
			return nil, syncErr
		}

		entries = append(entries, entry)
	}

	sortIdentityResponses(entries)

	return buildSyncResponse(entries), nil
}

// resolveIdentities picks the right resolver for the integration type.
func (s *Service) resolveIdentities(
	ctx context.Context,
	ictx *identityContext,
	byUser map[string]*models.UserIntegrationIdentity,
) (map[string]*identityHit, error) {
	if ictx.conn.Type == models.ConnectionTypeDiscord {
		return s.lookupDiscordIdentities(ctx, ictx.members, byUser), nil
	}

	token, err := s.slackAccessToken(ctx, ictx.conn)
	if err != nil {
		return nil, err
	}

	return s.lookupSlackIdentities(ctx, s.slackIdentityClientFor(token), ictx.members, byUser)
}

// skipAutoMatch reports whether a member's mapping is already settled by an
// admin, in which case the auto-match must not spend a lookup on them and must
// not get a vote.
func skipAutoMatch(user *models.User, byUser map[string]*models.UserIntegrationIdentity) bool {
	if user == nil {
		return true
	}

	existing := byUser[user.UID]

	return existing != nil && existing.Source == models.IdentitySourceManual
}

// lookupSlackIdentities resolves every member's email against the workspace.
func (s *Service) lookupSlackIdentities(
	ctx context.Context,
	client SlackIdentityLookup,
	members []*models.OrganizationMember,
	byUser map[string]*models.UserIntegrationIdentity,
) (map[string]*identityHit, error) {
	resolved := make(map[string]*identityHit, len(members))

	for _, member := range members {
		user := memberUser(member)
		if user == nil || strings.TrimSpace(user.Email) == "" || skipAutoMatch(user, byUser) {
			continue
		}

		slackUser, found, err := client.LookupUserByEmail(ctx, user.Email)
		if err != nil {
			return nil, fmt.Errorf("slack lookup for %s: %w", user.Email, err)
		}

		if found && slackUser != nil {
			resolved[user.UID] = &identityHit{ID: slackUser.ID, DisplayName: slackDisplayName(slackUser)}
		}
	}

	return resolved, nil
}

// lookupDiscordIdentities resolves members from the Discord sign-ins already on
// file (`user_providers`, provider type `discord`).
//
// Deliberately no API call: Discord has no "look up a user by email" endpoint
// for bots, and a guild member list would only tell us who is in the server,
// not which SolidPing account they are. Anyone who has ever pressed "Sign in
// with Discord" is therefore matched for free, and everyone else is mapped by
// an admin through the manual override — which is also why a failure here is
// impossible and the function returns no error.
func (s *Service) lookupDiscordIdentities(
	ctx context.Context,
	members []*models.OrganizationMember,
	byUser map[string]*models.UserIntegrationIdentity,
) map[string]*identityHit {
	resolved := make(map[string]*identityHit, len(members))

	for _, member := range members {
		user := memberUser(member)
		if user == nil || skipAutoMatch(user, byUser) {
			continue
		}

		providers, err := s.db.ListUserProvidersByUser(ctx, user.UID)
		if err != nil {
			continue
		}

		for _, provider := range providers {
			if provider.ProviderType != models.ProviderTypeDiscord || provider.ProviderID == "" {
				continue
			}

			resolved[user.UID] = &identityHit{
				ID:          provider.ProviderID,
				DisplayName: strings.TrimSpace(user.Name),
			}

			break
		}
	}

	return resolved
}

// slackIdentityClientFor builds the lookup client, falling back to the real
// Slack client when no factory was injected.
func (s *Service) slackIdentityClientFor(token string) SlackIdentityLookup {
	if s.slackIdentityClient != nil {
		return s.slackIdentityClient(token)
	}

	return defaultSlackIdentityClient(token)
}

// externalIDOwners maps each mapped external id to the user that owns it.
func externalIDOwners(rows []*models.UserIntegrationIdentity) map[string]string {
	owners := make(map[string]string, len(rows))
	for _, row := range rows {
		owners[row.ExternalID] = row.UserUID
	}

	return owners
}

// applyResolution turns one member's phase-1 answer into a persisted mapping
// and a reported status.
func (s *Service) applyResolution(
	ctx context.Context,
	conn *models.Integration,
	user *models.User,
	existing *models.UserIntegrationIdentity,
	hit *identityHit,
	owners map[string]string,
	claimCount map[string]int,
) (*MemberIdentityResponse, error) {
	// A manual mapping is the admin's answer; the auto-match does not get a
	// vote. Ditto for a member the provider could not place: keep whatever mapping
	// they already had rather than destroying it on one unhelpful answer, and
	// report "not found" only when there is nothing to keep.
	if hit == nil {
		return buildIdentityEntry(user, existing), nil
	}

	// Ambiguous either way round: the account already belongs to somebody else,
	// or two members' addresses both resolved to it in this very sync. Guessing
	// here would make an alert ping the wrong human, so nothing is written.
	ownedByOther := owners[hit.ID] != "" && owners[hit.ID] != user.UID
	if ownedByOther || claimCount[hit.ID] > 1 {
		entry := buildIdentityEntry(user, existing)
		entry.Status = IdentityStatusAmbiguous

		return entry, nil
	}

	identity := models.NewUserIntegrationIdentity(
		conn.OrganizationUID, conn.UID, user.UID,
		hit.ID, hit.DisplayName, models.IdentitySourceAuto,
	)

	if err := s.db.UpsertUserIntegrationIdentity(ctx, identity); err != nil {
		return nil, fmt.Errorf("upsert identity for %s: %w", user.Email, err)
	}

	owners[hit.ID] = user.UID

	return buildIdentityEntry(user, identity), nil
}

// slackDisplayName picks the friendliest label Slack gave us.
func slackDisplayName(user *slack.SlackUser) string {
	if user.RealName != "" {
		return user.RealName
	}

	return user.Name
}

// buildSyncResponse tallies the buckets over the final per-member state.
func buildSyncResponse(entries []*MemberIdentityResponse) *SyncIdentitiesResponse {
	resp := &SyncIdentitiesResponse{Data: entries}

	for _, entry := range entries {
		switch entry.Status {
		case IdentityStatusMatched:
			resp.MatchedCount++
		case IdentityStatusAmbiguous:
			resp.AmbiguousCount++
		default:
			resp.NotFoundCount++
		}
	}

	return resp
}

// SetIdentity records an admin's manual mapping of a member to a workspace
// user. The external id may be claimed by at most one member per integration.
func (s *Service) SetIdentity(
	ctx context.Context, orgSlug, integrationUID, userUID string, req SetIdentityRequest,
) (*MemberIdentityResponse, error) {
	ictx, err := s.loadIdentityContext(ctx, orgSlug, integrationUID)
	if err != nil {
		return nil, err
	}

	externalID := strings.TrimSpace(req.ExternalID)
	if externalID == "" {
		return nil, ErrIdentityExternalIDRequired
	}

	user := findMemberUser(ictx.members, userUID)
	if user == nil {
		return nil, ErrIdentityMemberNotFound
	}

	rows, err := s.db.ListUserIntegrationIdentities(ctx, ictx.conn.UID)
	if err != nil {
		return nil, fmt.Errorf("list identities: %w", err)
	}

	if owner, claimed := externalIDOwners(rows)[externalID]; claimed && owner != userUID {
		return nil, ErrIdentityAlreadyClaimed
	}

	identity := models.NewUserIntegrationIdentity(
		ictx.conn.OrganizationUID, ictx.conn.UID, userUID,
		externalID, strings.TrimSpace(req.DisplayName), models.IdentitySourceManual,
	)

	if err := s.db.UpsertUserIntegrationIdentity(ctx, identity); err != nil {
		return nil, fmt.Errorf("upsert identity: %w", err)
	}

	return buildIdentityEntry(user, identity), nil
}

// DeleteIdentity clears a member's mapping on an integration. Idempotent.
func (s *Service) DeleteIdentity(ctx context.Context, orgSlug, integrationUID, userUID string) error {
	ictx, err := s.loadIdentityContext(ctx, orgSlug, integrationUID)
	if err != nil {
		return err
	}

	if err := s.db.DeleteUserIntegrationIdentity(ctx, ictx.conn.UID, userUID); err != nil {
		return fmt.Errorf("delete identity: %w", err)
	}

	return nil
}

// findMemberUser returns the live user for a membership in this org.
func findMemberUser(members []*models.OrganizationMember, userUID string) *models.User {
	for _, member := range members {
		if member.UserUID != userUID {
			continue
		}

		return memberUser(member)
	}

	return nil
}
