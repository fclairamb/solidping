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

// TestOAuthSilentNoOpsRecordNothing. RFC 7009 says a revoke endpoint must never
// signal what happened — so the no-op branches must also stay out of the trail,
// or the audit log becomes the oracle the endpoint refuses to be.
func TestOAuthSilentNoOpsRecordNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setupOAuthService(t)

	code, err := f.svc.IssueAuthCode(f.ctx, f.grant(challengeFor(testVerifier), ScopeMCP))
	r.NoError(err)

	res, err := f.svc.ExchangeAuthCode(f.ctx, code, f.client.ClientID, testRedirectURI, testVerifier)
	r.NoError(err)

	before := len(f.authEvents(t))

	// Unknown token, and a grant presented by the wrong client.
	revoked, err := f.svc.RevokeGrant(f.ctx, "not-a-token", f.client.ClientID)
	r.NoError(err)
	r.False(revoked)

	revoked, err = f.svc.RevokeGrant(f.ctx, res.RefreshToken, "some-other-client")
	r.NoError(err)
	r.False(revoked)

	r.Len(f.authEvents(t), before)
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
