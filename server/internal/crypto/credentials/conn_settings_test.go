package credentials_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// slackConn builds a Slack integration row with the given public settings and
// private envelope.
func slackConn(public models.JSONMap, envelope *string) *models.Integration {
	return &models.Integration{
		UID:             "conn-1",
		OrganizationUID: "org-1",
		Type:            models.ConnectionTypeSlack,
		Settings:        public,
		SettingsPrivate: envelope,
	}
}

// TestConnectionSecretFieldsSlackCarriesAccessToken pins the declaration that
// makes the whole split happen. If access_token ever stopped being a Slack
// secret, every reader below would "pass" for the wrong reason.
func TestConnectionSecretFieldsSlackCarriesAccessToken(t *testing.T) {
	t.Parallel()

	require.Contains(t, credentials.ConnectionSecretFields(models.ConnectionTypeSlack), "access_token")
}

func TestOpenConnectionSettingsPreSplitRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	conn := slackConn(models.JSONMap{"team_id": "T1", "access_token": "xoxb-public"}, nil)

	merged, err := credentials.OpenConnectionSettings(t.Context(), nil, conn)
	r.NoError(err)
	r.Equal("xoxb-public", merged["access_token"])
	r.Equal("T1", merged["team_id"])
}

func TestOpenConnectionSettingsPlaintextEnvelopeWithoutService(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	envelope, err := credentials.SealPlaintext(map[string]any{"access_token": "xoxb-plain"})
	r.NoError(err)

	conn := slackConn(models.JSONMap{"team_id": "T1"}, &envelope)

	// Deliberately nil credentials service: a keyless deployment must still
	// read its own secrets.
	merged, err := credentials.OpenConnectionSettings(t.Context(), nil, conn)
	r.NoError(err)
	r.Equal("xoxb-plain", merged["access_token"])
	r.Equal("T1", merged["team_id"])

	// The input row must be untouched — callers may hold a cached pointer.
	r.NotContains(conn.Settings, "access_token")
}

func TestOpenConnectionSettingsSealedEnvelope(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	svc, err := credentials.NewService(newKey(t), newFakeStore())
	r.NoError(err)

	envelope, err := svc.EncryptForOrg(t.Context(), "org-1", map[string]any{"access_token": "xoxb-sealed"})
	r.NoError(err)

	conn := slackConn(models.JSONMap{"team_id": "T1"}, &envelope)

	merged, err := credentials.OpenConnectionSettings(t.Context(), svc, conn)
	r.NoError(err)
	r.Equal("xoxb-sealed", merged["access_token"])
	r.NotContains(conn.Settings, "access_token")
}

// TestOpenConnectionSettingsSealedWithoutKeyIsTyped proves a sealed envelope
// opened by a keyless process fails loudly and distinctly — an operator
// problem, not "this connection has no token".
func TestOpenConnectionSettingsSealedWithoutKeyIsTyped(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	svc, err := credentials.NewService(newKey(t), newFakeStore())
	r.NoError(err)

	envelope, err := svc.EncryptForOrg(t.Context(), "org-1", map[string]any{"access_token": "xoxb-sealed"})
	r.NoError(err)

	conn := slackConn(models.JSONMap{"team_id": "T1"}, &envelope)

	disabled, err := credentials.NewService(nil, newFakeStore())
	r.NoError(err)

	_, err = credentials.OpenConnectionSettings(t.Context(), disabled, conn)
	r.ErrorIs(err, credentials.ErrEncryptionDisabled)

	_, err = credentials.OpenConnectionSettings(t.Context(), nil, conn)
	r.ErrorIs(err, credentials.ErrEncryptionDisabled)
}

// TestSealConnectionSettingsRoundTrip proves the write half never leaves a
// secret in the public column, in both key modes, and that what it writes is
// exactly what the read half opens.
func TestSealConnectionSettingsRoundTrip(t *testing.T) {
	t.Parallel()

	effective := map[string]any{"team_id": "T1", "access_token": "xoxb-token"}

	for _, tc := range []struct {
		name    string
		withKey bool
	}{
		{name: "sealed", withKey: true},
		{name: "plaintext-fallback", withKey: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			var key []byte
			if tc.withKey {
				key = newKey(t)
			}

			svc, err := credentials.NewService(key, newFakeStore())
			r.NoError(err)

			sealed, err := credentials.SealConnectionSettings(
				t.Context(), svc, models.ConnectionTypeSlack, "org-1", effective,
			)
			r.NoError(err)
			r.NotContains(sealed.Public, "access_token", "the public column must never carry the bot token")
			r.Equal("T1", sealed.Public["team_id"])
			r.NotNil(sealed.Private)
			r.NotNil(sealed.PrivateKeys)

			var keys []string
			r.NoError(json.Unmarshal([]byte(*sealed.PrivateKeys), &keys))
			r.Equal([]string{"access_token"}, keys)

			conn := slackConn(models.JSONMap(sealed.Public), sealed.Private)

			merged, err := credentials.OpenConnectionSettings(t.Context(), svc, conn)
			r.NoError(err)
			r.Equal("xoxb-token", merged["access_token"])
		})
	}
}

// TestSealConnectionSettingsNoSecretClearsEnvelope covers the type that
// declares no secret at all: nothing to seal, no envelope written.
func TestSealConnectionSettingsNoSecretClearsEnvelope(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	sealed, err := credentials.SealConnectionSettings(
		t.Context(), nil, models.ConnectionTypeSlack, "org-1", map[string]any{"team_id": "T1"},
	)
	r.NoError(err)
	r.Nil(sealed.Private)
	r.Nil(sealed.PrivateKeys)
	r.Equal("T1", sealed.Public["team_id"])
}
