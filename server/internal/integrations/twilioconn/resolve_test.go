package twilioconn_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/integrations/twilioconn"
)

var errDecryptFailed = errors.New("bad envelope")

// stubCreds is a credentials.Service whose decrypt result is scripted.
type stubCreds struct {
	credentials.Service

	enabled bool
	secret  map[string]any
	err     error
}

func (s *stubCreds) Enabled() bool { return s.enabled }

func (s *stubCreds) DecryptForOrg(context.Context, string, string) (map[string]any, error) {
	return s.secret, s.err
}

func newDB(t *testing.T) (db.Service, string) {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("twilioconn-org", "Twilioconn Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	return dbSvc, org.UID
}

func addConn(
	t *testing.T, dbSvc db.Service, orgUID string, connType models.ConnectionType, settings models.JSONMap,
) *models.Integration {
	t.Helper()

	conn := models.NewIntegration(orgUID, connType, string(connType))
	conn.Enabled = true
	conn.Settings = settings
	require.NoError(t, dbSvc.CreateChannel(t.Context(), conn))

	return conn
}

func TestDecryptSettingsMap(t *testing.T) {
	t.Parallel()

	envelope := "sealed-envelope"
	empty := ""

	t.Run("plaintext settings are returned as is", func(t *testing.T) {
		t.Parallel()

		conn := &models.Integration{Settings: models.JSONMap{"sender": "ACME"}, SettingsPrivate: &empty}

		got, err := twilioconn.DecryptSettingsMap(t.Context(), nil, conn)
		require.NoError(t, err)
		require.Equal(t, "ACME", got["sender"])
	})

	t.Run("an envelope without a service is refused", func(t *testing.T) {
		t.Parallel()

		conn := &models.Integration{SettingsPrivate: &envelope}

		_, err := twilioconn.DecryptSettingsMap(t.Context(), nil, conn)
		require.ErrorIs(t, err, twilioconn.ErrEncryptionDisabled)
	})

	t.Run("an envelope needing a key is refused when encryption is off", func(t *testing.T) {
		t.Parallel()

		conn := &models.Integration{SettingsPrivate: &envelope}

		_, err := twilioconn.DecryptSettingsMap(t.Context(), &stubCreds{enabled: false}, conn)
		require.ErrorIs(t, err, twilioconn.ErrEncryptionDisabled)
	})

	t.Run("a decrypt failure is wrapped with the connection type", func(t *testing.T) {
		t.Parallel()

		conn := &models.Integration{Type: models.ConnectionTypeSMSFactor, SettingsPrivate: &envelope}

		_, err := twilioconn.DecryptSettingsMap(t.Context(), &stubCreds{enabled: true, err: errDecryptFailed}, conn)
		require.ErrorIs(t, err, errDecryptFailed)
		require.Contains(t, err.Error(), "decrypt smsfactor settings")
	})

	t.Run("the private part is merged over the public settings", func(t *testing.T) {
		t.Parallel()

		conn := &models.Integration{Settings: models.JSONMap{"sender": "ACME"}, SettingsPrivate: &envelope}

		got, err := twilioconn.DecryptSettingsMap(t.Context(),
			&stubCreds{enabled: true, secret: map[string]any{"token": "tok"}}, conn)
		require.NoError(t, err)
		require.Equal(t, "tok", got["token"])
		require.Equal(t, "ACME", got["sender"])
	})
}

func TestDecryptSettings(t *testing.T) {
	t.Parallel()

	t.Run("valid settings parse", func(t *testing.T) {
		t.Parallel()

		conn := &models.Integration{Settings: models.JSONMap{"account_sid": "AC1", "from_number": "+15550001111"}}

		ts, err := twilioconn.DecryptSettings(t.Context(), nil, conn)
		require.NoError(t, err)
		require.Equal(t, "AC1", ts.AccountSID)
	})

	t.Run("a decrypt error is propagated", func(t *testing.T) {
		t.Parallel()

		envelope := "sealed-envelope"
		conn := &models.Integration{SettingsPrivate: &envelope}

		_, err := twilioconn.DecryptSettings(t.Context(), nil, conn)
		require.ErrorIs(t, err, twilioconn.ErrEncryptionDisabled)
	})

	t.Run("settings of the wrong shape fail to parse", func(t *testing.T) {
		t.Parallel()

		conn := &models.Integration{Settings: models.JSONMap{"account_sid": 42}}

		_, err := twilioconn.DecryptSettings(t.Context(), nil, conn)
		require.Error(t, err)
		require.Contains(t, err.Error(), "parse twilio settings")
	})
}

func TestResolveDefault(t *testing.T) {
	t.Parallel()

	t.Run("no connection", func(t *testing.T) {
		t.Parallel()

		dbSvc, orgUID := newDB(t)

		_, _, err := twilioconn.ResolveDefault(t.Context(), dbSvc, nil, orgUID)
		require.ErrorIs(t, err, twilioconn.ErrNoTwilioConnection)
	})

	t.Run("an enabled connection resolves", func(t *testing.T) {
		t.Parallel()

		dbSvc, orgUID := newDB(t)
		created := addConn(t, dbSvc, orgUID, models.ConnectionTypeTwilio,
			models.JSONMap{"account_sid": "AC1", "from_number": "+15550001111"})

		conn, settings, err := twilioconn.ResolveDefault(t.Context(), dbSvc, nil, orgUID)
		require.NoError(t, err)
		require.Equal(t, created.UID, conn.UID)
		require.Equal(t, "AC1", settings.AccountSID)
	})

	t.Run("a connection with unreadable settings fails", func(t *testing.T) {
		t.Parallel()

		dbSvc, orgUID := newDB(t)
		addConn(t, dbSvc, orgUID, models.ConnectionTypeTwilio, models.JSONMap{"account_sid": 42})

		_, _, err := twilioconn.ResolveDefault(t.Context(), dbSvc, nil, orgUID)
		require.Error(t, err)
		require.NotErrorIs(t, err, twilioconn.ErrNoTwilioConnection)
	})

	t.Run("a database failure is reported", func(t *testing.T) {
		t.Parallel()

		dbSvc, orgUID := newDB(t)
		require.NoError(t, dbSvc.Close())

		_, _, err := twilioconn.ResolveDefault(t.Context(), dbSvc, nil, orgUID)
		require.Error(t, err)
		require.Contains(t, err.Error(), "list twilio connections")
	})
}

func TestDefaultEnabledConnectionIgnoresOtherTypes(t *testing.T) {
	t.Parallel()

	dbSvc, orgUID := newDB(t)
	addConn(t, dbSvc, orgUID, models.ConnectionTypeTwilio, models.JSONMap{})

	conn, err := twilioconn.DefaultEnabledConnection(t.Context(), dbSvc, orgUID, models.ConnectionTypeSMSFactor)
	require.NoError(t, err)
	require.Nil(t, conn)
}
