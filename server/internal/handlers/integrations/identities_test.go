package integrations_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
	"github.com/fclairamb/solidping/server/internal/integrations/slack"
)

// errSlackDown stands in for a Slack transport/auth failure.
var errSlackDown = errors.New("slack is down")

// fakeSlackLookup is a scripted users.lookupByEmail. `byEmail` holds the hits;
// `fail` makes every call error so the failure path can be exercised.
type fakeSlackLookup struct {
	byEmail map[string]*slack.SlackUser
	fail    bool
	calls   int
}

func (f *fakeSlackLookup) LookupUserByEmail(
	_ context.Context, email string,
) (*slack.SlackUser, bool, error) {
	f.calls++

	if f.fail {
		return nil, false, errSlackDown
	}

	user, ok := f.byEmail[email]
	if !ok {
		return nil, false, nil
	}

	return user, true, nil
}

// identityFixture is an org with a connected Slack integration and members.
type identityFixture struct {
	svc    *integrations.Service
	dbSvc  db.Service
	org    *models.Organization
	conn   *models.Integration
	lookup *fakeSlackLookup
}

// addMember creates a user and its membership, returning the user.
func (f *identityFixture) addMember(ctx context.Context, t *testing.T, email, name string) *models.User {
	t.Helper()

	user := models.NewUser(email)
	user.Name = name
	require.NoError(t, f.dbSvc.CreateUser(ctx, user))
	require.NoError(t, f.dbSvc.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(f.org.UID, user.UID, models.MemberRoleUser)))

	return user
}

// entryFor finds a member's row in an identity listing.
func entryFor(entries []*integrations.MemberIdentityResponse, userUID string) *integrations.MemberIdentityResponse {
	for _, entry := range entries {
		if entry.UserUID == userUID {
			return entry
		}
	}

	return nil
}

func newIdentityFixture(ctx context.Context, t *testing.T, slug string) *identityFixture {
	t.Helper()

	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	org := models.NewOrganization(slug, "Identity Test Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	settings := &models.SlackSettings{
		TeamID:      "T1",
		TeamName:    "Acme",
		AccessToken: "xoxb-test",
	}
	settingsMap, err := settings.ToJSONMap()
	r.NoError(err)

	conn := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "Acme Slack")
	conn.Settings = settingsMap
	r.NoError(dbSvc.CreateChannel(ctx, conn))

	svc := integrations.NewService(dbSvc, creds, nil, nil)
	lookup := &fakeSlackLookup{byEmail: map[string]*slack.SlackUser{}}
	svc.SetSlackIdentityClient(func(_ string) integrations.SlackIdentityLookup { return lookup })

	return &identityFixture{svc: svc, dbSvc: dbSvc, org: org, conn: conn, lookup: lookup}
}

// TestSyncIdentitiesBuckets is the table-driven auto-match spec: every member
// lands in exactly one bucket, and an ambiguous hit is never written.
func TestSyncIdentitiesBuckets(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newIdentityFixture(ctx, t, "sync-buckets")

	matched := fx.addMember(ctx, t, "alice@acme.test", "Alice")
	missing := fx.addMember(ctx, t, "bob@acme.test", "Bob")
	carol := fx.addMember(ctx, t, "carol@acme.test", "Carol")
	dave := fx.addMember(ctx, t, "dave@acme.test", "Dave")

	fx.lookup.byEmail["alice@acme.test"] = &slack.SlackUser{ID: "U-ALICE", RealName: "Alice A"}
	// Carol and Dave both resolve to the SAME Slack account. Neither may be
	// written, and the outcome must not depend on which member is visited
	// first — hence "both ambiguous", not "first one wins".
	fx.lookup.byEmail["carol@acme.test"] = &slack.SlackUser{ID: "U-DUP", RealName: "Dup"}
	fx.lookup.byEmail["dave@acme.test"] = &slack.SlackUser{ID: "U-DUP", RealName: "Dup"}

	resp, err := fx.svc.SyncIdentities(ctx, fx.org.Slug, fx.conn.UID)
	r.NoError(err)
	r.Len(resp.Data, 4)
	r.Equal(1, resp.MatchedCount)
	r.Equal(1, resp.NotFoundCount)
	r.Equal(2, resp.AmbiguousCount)

	r.Equal(integrations.IdentityStatusMatched, entryFor(resp.Data, matched.UID).Status)
	r.Equal("U-ALICE", entryFor(resp.Data, matched.UID).ExternalID)
	r.Equal(models.IdentitySourceAuto, entryFor(resp.Data, matched.UID).Source)
	r.Equal(integrations.IdentityStatusNotFound, entryFor(resp.Data, missing.UID).Status)
	r.Equal(integrations.IdentityStatusAmbiguous, entryFor(resp.Data, carol.UID).Status)
	r.Equal(integrations.IdentityStatusAmbiguous, entryFor(resp.Data, dave.UID).Status)

	// The ambiguous members must have NO row written — the negative half of
	// "surfaced in the UI, never silently guessed".
	for _, user := range []*models.User{carol, dave} {
		stored, storeErr := fx.dbSvc.GetUserIntegrationIdentity(ctx, fx.conn.UID, user.UID)
		r.NoError(storeErr)
		r.Nil(stored)
	}
}

// TestSyncIdentitiesRefusesAccountOwnedByAnotherMember covers the other
// ambiguity shape: the Slack account an email resolves to is already mapped to
// somebody else (here by an admin's manual override, which the sync must not
// steal).
func TestSyncIdentitiesRefusesAccountOwnedByAnotherMember(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newIdentityFixture(ctx, t, "sync-owned")

	owner := fx.addMember(ctx, t, "owner@acme.test", "Owner")
	intruder := fx.addMember(ctx, t, "intruder@acme.test", "Intruder")

	_, err := fx.svc.SetIdentity(ctx, fx.org.Slug, fx.conn.UID, owner.UID,
		integrations.SetIdentityRequest{ExternalID: "U-SHARED"})
	r.NoError(err)

	fx.lookup.byEmail["intruder@acme.test"] = &slack.SlackUser{ID: "U-SHARED", RealName: "Shared"}

	resp, err := fx.svc.SyncIdentities(ctx, fx.org.Slug, fx.conn.UID)
	r.NoError(err)
	r.Equal(integrations.IdentityStatusAmbiguous, entryFor(resp.Data, intruder.UID).Status)
	r.Equal("U-SHARED", entryFor(resp.Data, owner.UID).ExternalID)

	stored, err := fx.dbSvc.GetUserIntegrationIdentity(ctx, fx.conn.UID, intruder.UID)
	r.NoError(err)
	r.Nil(stored)
}

// TestSyncIdentitiesPreservesManualOverride pins that a re-sync never
// overwrites an admin's explicit choice — and never even asks Slack about it.
func TestSyncIdentitiesPreservesManualOverride(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newIdentityFixture(ctx, t, "sync-manual")

	alice := fx.addMember(ctx, t, "alice@acme.test", "Alice")
	fx.lookup.byEmail["alice@acme.test"] = &slack.SlackUser{ID: "U-AUTO", RealName: "auto"}

	_, err := fx.svc.SetIdentity(ctx, fx.org.Slug, fx.conn.UID, alice.UID,
		integrations.SetIdentityRequest{ExternalID: "U-MANUAL", DisplayName: "Alice (manual)"})
	r.NoError(err)

	before := fx.lookup.calls

	resp, err := fx.svc.SyncIdentities(ctx, fx.org.Slug, fx.conn.UID)
	r.NoError(err)
	r.Equal(before, fx.lookup.calls, "a manual mapping must not trigger a Slack lookup")

	entry := entryFor(resp.Data, alice.UID)
	r.Equal("U-MANUAL", entry.ExternalID)
	r.Equal(models.IdentitySourceManual, entry.Source)
}

// TestSyncIdentitiesKeepsMappingWhenLookupMisses guards against a transient
// "not found" wiping a working mapping.
func TestSyncIdentitiesKeepsMappingWhenLookupMisses(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newIdentityFixture(ctx, t, "sync-keeps")

	alice := fx.addMember(ctx, t, "alice@acme.test", "Alice")
	fx.lookup.byEmail["alice@acme.test"] = &slack.SlackUser{ID: "U-ALICE", RealName: "Alice A"}

	_, err := fx.svc.SyncIdentities(ctx, fx.org.Slug, fx.conn.UID)
	r.NoError(err)

	// Slack now knows nothing about her.
	delete(fx.lookup.byEmail, "alice@acme.test")

	resp, err := fx.svc.SyncIdentities(ctx, fx.org.Slug, fx.conn.UID)
	r.NoError(err)
	r.Equal("U-ALICE", entryFor(resp.Data, alice.UID).ExternalID)
	r.Equal(integrations.IdentityStatusMatched, entryFor(resp.Data, alice.UID).Status)
}

// TestSyncIdentitiesSurfacesLookupFailure: a transport/auth failure is a real
// error the admin must see, not a silent empty result.
func TestSyncIdentitiesSurfacesLookupFailure(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newIdentityFixture(ctx, t, "sync-fails")

	fx.addMember(ctx, t, "alice@acme.test", "Alice")
	fx.lookup.fail = true

	_, err := fx.svc.SyncIdentities(ctx, fx.org.Slug, fx.conn.UID)
	r.ErrorIs(err, errSlackDown)
}

// TestSetIdentityRejectsClaimedExternalID: one Slack account maps to at most
// one member, so mentions can never name two people as the same human.
func TestSetIdentityRejectsClaimedExternalID(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newIdentityFixture(ctx, t, "set-claimed")

	alice := fx.addMember(ctx, t, "alice@acme.test", "Alice")
	bob := fx.addMember(ctx, t, "bob@acme.test", "Bob")

	_, err := fx.svc.SetIdentity(ctx, fx.org.Slug, fx.conn.UID, alice.UID,
		integrations.SetIdentityRequest{ExternalID: "U-SHARED"})
	r.NoError(err)

	_, err = fx.svc.SetIdentity(ctx, fx.org.Slug, fx.conn.UID, bob.UID,
		integrations.SetIdentityRequest{ExternalID: "U-SHARED"})
	r.ErrorIs(err, integrations.ErrIdentityAlreadyClaimed)
}

// TestSetIdentityValidation covers the two refusals that are not about
// conflicts: an empty id and a non-member target.
func TestSetIdentityValidation(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newIdentityFixture(ctx, t, "set-validation")

	alice := fx.addMember(ctx, t, "alice@acme.test", "Alice")

	_, err := fx.svc.SetIdentity(ctx, fx.org.Slug, fx.conn.UID, alice.UID,
		integrations.SetIdentityRequest{ExternalID: "   "})
	r.ErrorIs(err, integrations.ErrIdentityExternalIDRequired)

	outsider := models.NewUser("mallory@elsewhere.test")
	r.NoError(fx.dbSvc.CreateUser(ctx, outsider))

	_, err = fx.svc.SetIdentity(ctx, fx.org.Slug, fx.conn.UID, outsider.UID,
		integrations.SetIdentityRequest{ExternalID: "U-M"})
	r.ErrorIs(err, integrations.ErrIdentityMemberNotFound)
}

// TestDeleteIdentityClearsMapping also proves the delete is idempotent.
func TestDeleteIdentityClearsMapping(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newIdentityFixture(ctx, t, "delete-identity")

	alice := fx.addMember(ctx, t, "alice@acme.test", "Alice")

	_, err := fx.svc.SetIdentity(ctx, fx.org.Slug, fx.conn.UID, alice.UID,
		integrations.SetIdentityRequest{ExternalID: "U-ALICE"})
	r.NoError(err)

	r.NoError(fx.svc.DeleteIdentity(ctx, fx.org.Slug, fx.conn.UID, alice.UID))
	r.NoError(fx.svc.DeleteIdentity(ctx, fx.org.Slug, fx.conn.UID, alice.UID))

	stored, err := fx.dbSvc.GetUserIntegrationIdentity(ctx, fx.conn.UID, alice.UID)
	r.NoError(err)
	r.Nil(stored)
}

// TestListIdentitiesNeverCallsSlack: the read path must be cheap enough to
// render on every panel open.
func TestListIdentitiesNeverCallsSlack(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newIdentityFixture(ctx, t, "list-identities")

	alice := fx.addMember(ctx, t, "alice@acme.test", "Alice")
	fx.addMember(ctx, t, "bob@acme.test", "Bob")

	_, err := fx.svc.SetIdentity(ctx, fx.org.Slug, fx.conn.UID, alice.UID,
		integrations.SetIdentityRequest{ExternalID: "U-ALICE"})
	r.NoError(err)

	resp, err := fx.svc.ListIdentities(ctx, fx.org.Slug, fx.conn.UID)
	r.NoError(err)
	r.Zero(fx.lookup.calls)
	r.Len(resp.Data, 2)
	// Ordered by display label: Alice before Bob.
	r.Equal("Alice", resp.Data[0].Name)
	r.Equal("Bob", resp.Data[1].Name)
	r.Equal(integrations.IdentityStatusMatched, resp.Data[0].Status)
	r.Equal(integrations.IdentityStatusNotFound, resp.Data[1].Status)
}

// TestIdentitiesRejectNonSlackIntegration keeps the endpoint honest about its
// scope: Teams identities are explicitly out of scope for this spec.
func TestIdentitiesRejectNonSlackIntegration(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newIdentityFixture(ctx, t, "identities-type")

	hook := models.NewIntegration(fx.org.UID, models.ConnectionTypeWebhook, "hook")
	hook.Settings = models.JSONMap{"webhook_url": "https://example.com/x"}
	r.NoError(fx.dbSvc.CreateChannel(ctx, hook))

	_, err := fx.svc.ListIdentities(ctx, fx.org.Slug, hook.UID)
	r.ErrorIs(err, integrations.ErrIdentitiesUnsupportedType)
}

// TestSyncIdentitiesRequiresConnectedSlack: a tokenless stub cannot be synced,
// and must say so rather than surfacing an opaque Slack auth failure.
func TestSyncIdentitiesRequiresConnectedSlack(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newIdentityFixture(ctx, t, "identities-tokenless")

	stub := models.NewIntegration(fx.org.UID, models.ConnectionTypeSlack, "stub")
	r.NoError(fx.dbSvc.CreateChannel(ctx, stub))

	_, err := fx.svc.SyncIdentities(ctx, fx.org.Slug, stub.UID)
	r.ErrorIs(err, integrations.ErrSlackNotConnected)
}

// discordIdentityFixture is an org with a bot-mode Discord integration.
type discordIdentityFixture struct {
	svc   *integrations.Service
	dbSvc db.Service
	org   *models.Organization
	conn  *models.Integration
}

func (f *discordIdentityFixture) addMember(
	ctx context.Context, t *testing.T, email, name string,
) *models.User {
	t.Helper()

	user := models.NewUser(email)
	user.Name = name
	require.NoError(t, f.dbSvc.CreateUser(ctx, user))
	require.NoError(t, f.dbSvc.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(f.org.UID, user.UID, models.MemberRoleUser)))

	return user
}

// signedInWithDiscord records the user_providers row "Sign in with Discord"
// writes, which is the only source the Discord auto-match reads.
func (f *discordIdentityFixture) signedInWithDiscord(
	ctx context.Context, t *testing.T, user *models.User, discordUserID string,
) {
	t.Helper()

	require.NoError(t, f.dbSvc.CreateUserProvider(
		ctx, models.NewUserProvider(user.UID, models.ProviderTypeDiscord, discordUserID)))
}

func newDiscordIdentityFixture(
	ctx context.Context, t *testing.T, slug string, settings *models.DiscordSettings,
) *discordIdentityFixture {
	t.Helper()

	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	org := models.NewOrganization(slug, "Discord Identity Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	settingsMap, err := settings.ToJSONMap()
	r.NoError(err)

	conn := models.NewIntegration(org.UID, models.ConnectionTypeDiscord, "acme discord")
	conn.Settings = settingsMap
	r.NoError(dbSvc.CreateChannel(ctx, conn))

	return &discordIdentityFixture{
		svc:   integrations.NewService(dbSvc, creds, nil, nil),
		dbSvc: dbSvc,
		org:   org,
		conn:  conn,
	}
}

// TestSyncIdentitiesDiscordUsesSignIns proves the Discord auto-match: a member
// who has signed in with Discord is mapped for free, with no API call (Discord
// has no bot-facing "look up a user by email"), and a member who has not is
// simply reported not-found for an admin to map by hand.
func TestSyncIdentitiesDiscordUsesSignIns(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	fx := newDiscordIdentityFixture(ctx, t, "discord-ids", &models.DiscordSettings{
		GuildID: "G-ACME", GuildName: "acme", ChannelID: "C-ALERTS",
	})

	alice := fx.addMember(ctx, t, "alice@acme.test", "Alice")
	bob := fx.addMember(ctx, t, "bob@acme.test", "Bob")

	fx.signedInWithDiscord(ctx, t, alice, "111222333444555666")

	resp, err := fx.svc.SyncIdentities(ctx, fx.org.Slug, fx.conn.UID)
	r.NoError(err)
	r.Len(resp.Data, 2)
	r.Equal(1, resp.MatchedCount)
	r.Equal(1, resp.NotFoundCount)

	aliceEntry := entryFor(resp.Data, alice.UID)
	r.NotNil(aliceEntry)
	r.Equal(integrations.IdentityStatusMatched, aliceEntry.Status)
	r.Equal("111222333444555666", aliceEntry.ExternalID)

	bobEntry := entryFor(resp.Data, bob.UID)
	r.NotNil(bobEntry)
	r.Equal(integrations.IdentityStatusNotFound, bobEntry.Status)
}

// TestSyncIdentitiesDiscordWebhookModeRejected: a legacy webhook integration
// cannot render a `<@id>` ping at all, so offering identity mapping there would
// be a setting that quietly does nothing.
func TestSyncIdentitiesDiscordWebhookModeRejected(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	fx := newDiscordIdentityFixture(ctx, t, "discord-webhook-ids", &models.DiscordSettings{
		WebhookURL: "https://discord.com/api/webhooks/1/acme-legacy",
	})

	_, err := fx.svc.SyncIdentities(ctx, fx.org.Slug, fx.conn.UID)
	r.ErrorIs(err, integrations.ErrIdentitiesUnsupportedType)
}
