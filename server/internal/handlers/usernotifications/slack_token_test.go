package usernotifications

import (
	"context"
	"crypto/rand"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// These tests pin the regression of spec 2026-09-18-02: the Slack bot token
// lives in the encrypted `settings_private` envelope, and the test-DM button
// used to read the public settings map only — so a perfectly installed Slack
// app answered "slack client not configured".

// recordingSlackDM captures the token the service resolved, which is the whole
// point: asserting "no error" would pass against a sender handed "".
type recordingSlackDM struct {
	token  string
	userID string
	calls  int
}

func (r *recordingSlackDM) SendDMTest(_ context.Context, accessToken, slackUserID string) error {
	r.calls++
	r.token = accessToken
	r.userID = slackUserID

	return nil
}

// slackDMEnv builds a db-backed service with one org holding a single enabled
// Slack integration whose settings are supplied by the caller.
func slackDMEnv(
	t *testing.T, creds credentials.Service, settings models.JSONMap, envelope *string,
) (*Service, *models.Organization) {
	t.Helper()

	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("slack-dm-org", "Slack DM Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	conn := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "workspace")
	conn.Enabled = true
	conn.IsDefault = true
	conn.Settings = settings
	conn.SettingsPrivate = envelope
	require.NoError(t, dbSvc.CreateChannel(ctx, conn))

	return NewService(dbSvc, creds), org
}

func testCredsService(t *testing.T) credentials.Service {
	t.Helper()

	key := make([]byte, 32)
	_, err := io.ReadFull(rand.Reader, key)
	require.NoError(t, err)

	svc, err := credentials.NewService(key, newMemoryDEKStore())
	require.NoError(t, err)

	return svc
}

// newMemoryDEKStore is an in-memory DEKStore so the sealed case exercises the
// real encrypt/decrypt path without a database.
type memoryDEKStore struct{ data map[string][]byte }

func newMemoryDEKStore() *memoryDEKStore {
	return &memoryDEKStore{data: map[string][]byte{}}
}

func (s *memoryDEKStore) LoadDEK(_ context.Context, orgUID string) ([]byte, bool, error) {
	v, ok := s.data[orgUID]

	return v, ok, nil
}

func (s *memoryDEKStore) SaveDEK(_ context.Context, orgUID string, wrapped []byte) error {
	s.data[orgUID] = wrapped

	return nil
}

// TestDispatchTestSlack_TokenOnlyInPrivateEnvelope is the regression proper:
// the public settings hold no access_token at all, exactly as every row does
// after the startup split sweep.
func TestDispatchTestSlack_TokenOnlyInPrivateEnvelope(t *testing.T) {
	t.Parallel()

	t.Run("plaintext envelope", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		envelope, err := credentials.SealPlaintext(map[string]any{"access_token": "xoxb-plain"})
		r.NoError(err)

		svc, org := slackDMEnv(t, nil, models.JSONMap{"team_id": "T1"}, &envelope)
		sender := &recordingSlackDM{}

		r.NoError(svc.dispatchTestSlack(context.Background(), org.UID, "U123", sender))
		r.Equal(1, sender.calls)
		r.Equal("xoxb-plain", sender.token)
		r.Equal("U123", sender.userID)
	})

	t.Run("sealed envelope", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		ctx := context.Background()

		creds := testCredsService(t)

		svc, org := slackDMEnv(t, creds, models.JSONMap{"team_id": "T1"}, nil)

		// Seal against the org that actually exists, then rewrite the row.
		envelope, err := creds.EncryptForOrg(ctx, org.UID, map[string]any{"access_token": "xoxb-sealed"})
		r.NoError(err)

		conn, err := svc.db.GetSlackChannelForOrg(ctx, org.UID)
		r.NoError(err)
		r.NoError(svc.db.UpdateChannel(ctx, conn.UID, &models.IntegrationUpdate{SettingsPrivate: &envelope}))

		sender := &recordingSlackDM{}
		r.NoError(svc.dispatchTestSlack(ctx, org.UID, "U123", sender))
		r.Equal("xoxb-sealed", sender.token)
	})
}

// TestDispatchTestSlack_PreSplitRowStillWorks is the positive control: a row
// written before the split, with the token in public settings, must keep
// working with no migration.
func TestDispatchTestSlack_PreSplitRowStillWorks(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	svc, org := slackDMEnv(t, nil, models.JSONMap{"team_id": "T1", "access_token": "xoxb-public"}, nil)
	sender := &recordingSlackDM{}

	r.NoError(svc.dispatchTestSlack(context.Background(), org.UID, "U123", sender))
	r.Equal("xoxb-public", sender.token)
}

// TestDispatchTestSlack_NoTokenAnywhere is the negative: a genuinely
// token-less stub still refuses, and says the app is not installed.
func TestDispatchTestSlack_NoTokenAnywhere(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	svc, org := slackDMEnv(t, nil, models.JSONMap{"team_id": "T1"}, nil)
	sender := &recordingSlackDM{}

	err := svc.dispatchTestSlack(context.Background(), org.UID, "U123", sender)
	r.ErrorIs(err, ErrSlackClientNotConfigured)
	r.Equal(0, sender.calls)
	r.Contains(ErrSlackClientNotConfigured.Error(), "not installed for this organization")
}

// TestDispatchTestSlack_SealedWithoutKeyIsNotMistakenForMissing proves the
// two failures stay distinct: a process with no master key must not report an
// encrypted token as "app not installed".
func TestDispatchTestSlack_SealedWithoutKeyIsNotMistakenForMissing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	creds := testCredsService(t)
	svc, org := slackDMEnv(t, nil, models.JSONMap{"team_id": "T1"}, nil)

	envelope, err := creds.EncryptForOrg(ctx, org.UID, map[string]any{"access_token": "xoxb-sealed"})
	r.NoError(err)

	conn, err := svc.db.GetSlackChannelForOrg(ctx, org.UID)
	r.NoError(err)
	r.NoError(svc.db.UpdateChannel(ctx, conn.UID, &models.IntegrationUpdate{SettingsPrivate: &envelope}))

	err = svc.dispatchTestSlack(ctx, org.UID, "U123", &recordingSlackDM{})
	r.ErrorIs(err, credentials.ErrEncryptionDisabled)
	r.NotErrorIs(err, ErrSlackClientNotConfigured)
}
