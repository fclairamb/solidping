package oauth

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// authEvents returns the org's auth.* audit trail, newest first.
func (f oauthFixture) authEvents(t *testing.T) []*models.Event {
	t.Helper()

	rows, err := f.db.ListEvents(f.ctx, &models.ListEventsFilter{
		OrganizationUID:   f.org.UID,
		EventTypePrefixes: []string{"auth"},
		Limit:             100,
	})
	require.NoError(t, err)

	return rows
}

// TestOAuthGrantIsAudited. An OAuth grant hands an external client the right to
// act as this user, in this org, for the listed scopes, until it is revoked —
// a sibling of "a personal access token was minted", and exactly the fact the
// spec exists to make answerable ("who was granted access?").
//
// Before this the whole MCP authorization path was invisible in the trail, and
// the session guard in the auth package could never have noticed: it scans
// only its own directory.
func TestOAuthGrantIsAudited(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setupOAuthService(t)

	code, err := f.svc.IssueAuthCode(f.ctx, f.grant(challengeFor(testVerifier), ScopeMCP))
	r.NoError(err)

	_, err = f.svc.ExchangeAuthCode(f.ctx, code, f.client.ClientID, testRedirectURI, testVerifier)
	r.NoError(err)

	events := f.authEvents(t)
	r.Len(events, 1)
	r.Equal(models.EventTypeAuthTokenCreated, events[0].EventType)
	r.Equal(auditTokenKindOAuthGrant, events[0].Payload["token_kind"])
	r.Equal(f.client.ClientID, events[0].Payload[paramClientID])
	r.Equal(ScopeMCP, events[0].Payload[paramScope])
	r.Equal(grantAuthorizationCode, events[0].Payload["grant_type"])

	// Attributed to the user who consented, not to "system".
	r.Equal(models.ActorTypeUser, events[0].ActorType)
	r.NotNil(events[0].ActorUID)
	r.Equal(f.user.UID, *events[0].ActorUID)
}

// TestOAuthGrantRotationIsSilent. ExchangeRefreshToken re-mints a pair every
// access-token TTL for the life of the grant; a row per rotation would bury
// the grant that matters under thousands saying nothing new.
//
// The positive control is the first assertion: the ORIGINAL grant is recorded,
// so this is not passing because emission is broken outright.
func TestOAuthGrantRotationIsSilent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setupOAuthService(t)

	code, err := f.svc.IssueAuthCode(f.ctx, f.grant(challengeFor(testVerifier), ScopeMCP))
	r.NoError(err)

	res, err := f.svc.ExchangeAuthCode(f.ctx, code, f.client.ClientID, testRedirectURI, testVerifier)
	r.NoError(err)
	r.Len(f.authEvents(t), 1, "positive control: the initial grant is recorded")

	refresh := res.RefreshToken

	for range 3 {
		rotated, rotateErr := f.svc.ExchangeRefreshToken(f.ctx, refresh, f.client.ClientID)
		r.NoError(rotateErr)

		refresh = rotated.RefreshToken
	}

	r.Len(f.authEvents(t), 1, "rotations must not each write a row")
}

// TestOAuthGrantRevocationIsAudited — the other half of "who was granted
// access": when it was taken away.
func TestOAuthGrantRevocationIsAudited(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setupOAuthService(t)

	code, err := f.svc.IssueAuthCode(f.ctx, f.grant(challengeFor(testVerifier), ScopeMCP))
	r.NoError(err)

	res, err := f.svc.ExchangeAuthCode(f.ctx, code, f.client.ClientID, testRedirectURI, testVerifier)
	r.NoError(err)

	revoked, err := f.svc.RevokeGrant(f.ctx, res.RefreshToken, f.client.ClientID)
	r.NoError(err)
	r.True(revoked)

	events := f.authEvents(t)
	r.Len(events, 2)
	r.Equal(models.EventTypeAuthTokenRevoked, events[0].EventType)
	r.Equal(auditTokenKindOAuthGrant, events[0].Payload["token_kind"])
	r.Equal(f.client.ClientID, events[0].Payload[paramClientID])

	// A repeat revocation is an RFC 7009 no-op and must not manufacture a
	// second row claiming a revocation that did not happen.
	repeat, err := f.svc.RevokeGrant(f.ctx, res.RefreshToken, f.client.ClientID)
	r.NoError(err)
	r.False(repeat)
	r.Len(f.authEvents(t), 2)
}

// TestOAuthUnknownTokenRevokeRecordsNothing. An unknown or already-revoked
// token has NO ROW, so there is no organization to scope an org-scoped event
// to — recording one would mean letting an unauthenticated caller write into
// some org's trail by guessing a token. That, not the oracle argument, is why
// this branch is silent.
func TestOAuthUnknownTokenRevokeRecordsNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setupOAuthService(t)

	code, err := f.svc.IssueAuthCode(f.ctx, f.grant(challengeFor(testVerifier), ScopeMCP))
	r.NoError(err)

	res, err := f.svc.ExchangeAuthCode(f.ctx, code, f.client.ClientID, testRedirectURI, testVerifier)
	r.NoError(err)

	before := len(f.authEvents(t))
	r.Positive(before, "positive control: the grant itself was recorded")

	revoked, err := f.svc.RevokeGrant(f.ctx, "not-a-token", f.client.ClientID)
	r.NoError(err)
	r.False(revoked)

	// Already revoked: the row is soft-deleted, so the lookup misses exactly
	// as it does for an unknown token.
	_, err = f.svc.RevokeGrant(f.ctx, res.RefreshToken, f.client.ClientID)
	r.NoError(err)

	afterRevoke := len(f.authEvents(t))

	repeat, err := f.svc.RevokeGrant(f.ctx, res.RefreshToken, f.client.ClientID)
	r.NoError(err)
	r.False(repeat)

	r.Len(f.authEvents(t), afterRevoke, "an unknown / already-revoked token writes nothing")
}

// TestOAuthWrongClientRevokeIsRecorded. "Client A presented client B's grant"
// is a leaked token being tried, or a confused-deputy bug — a real security
// signal, and one this branch CAN record because the row (and therefore the
// org) exists.
//
// It is a distinct event type rather than an auth.token_revoked carrying a
// `result` field, because nothing was revoked: auth.token_revoked has to mean
// what it says or every reader has to check a discriminator before believing
// it.
func TestOAuthWrongClientRevokeIsRecorded(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setupOAuthService(t)

	code, err := f.svc.IssueAuthCode(f.ctx, f.grant(challengeFor(testVerifier), ScopeMCP))
	r.NoError(err)

	res, err := f.svc.ExchangeAuthCode(f.ctx, code, f.client.ClientID, testRedirectURI, testVerifier)
	r.NoError(err)

	revoked, err := f.svc.RevokeGrant(f.ctx, res.RefreshToken, "some-other-client")
	r.NoError(err)
	r.False(revoked, "the caller still gets the indistinguishable RFC 7009 no-op")

	events := f.authEvents(t)
	r.Len(events, 2)

	misuse := events[0]
	r.Equal(models.EventTypeAuthTokenMisuse, misuse.EventType)
	r.Equal(misuseReasonWrongClient, misuse.Payload["reason"])
	r.Equal(f.client.ClientID, misuse.Payload[paramClientID])
	r.Equal("some-other-client", misuse.Payload["presented_client_id"])

	// NOT attributed to the grant's owner: that user did not do this, and a
	// false accusation in a record people act on is worse than no record.
	r.Equal(models.ActorTypeSystem, misuse.ActorType)
	r.Nil(misuse.ActorUID)

	// And the grant is untouched — the misuse event must not read as a
	// revocation that happened.
	r.NotEqual(models.EventTypeAuthTokenRevoked, misuse.EventType)

	stillLive, err := f.svc.RevokeGrant(f.ctx, res.RefreshToken, f.client.ClientID)
	r.NoError(err)
	r.True(stillLive, "the rightful client can still revoke it")
}

// TestOAuthGrantPayloadCarriesNoCredential — the payload assembled by this
// package must not carry the access or refresh token under any key.
func TestOAuthGrantPayloadCarriesNoCredential(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setupOAuthService(t)

	code, err := f.svc.IssueAuthCode(f.ctx, f.grant(challengeFor(testVerifier), ScopeMCP))
	r.NoError(err)

	res, err := f.svc.ExchangeAuthCode(f.ctx, code, f.client.ClientID, testRedirectURI, testVerifier)
	r.NoError(err)

	events := f.authEvents(t)
	r.Len(events, 1)

	// Positive control: the fields the trail is FOR are present.
	r.NotEmpty(events[0].Payload[paramClientID])

	for key, value := range events[0].Payload {
		r.Falsef(audit.IsSensitiveKey(key), "payload key %q would carry a secret", key)

		text, ok := value.(string)
		if !ok {
			continue
		}

		r.NotContains(text, res.RefreshToken)
		r.NotContains(text, res.AccessToken)
	}
}
