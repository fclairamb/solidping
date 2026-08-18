package credentials_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// fakeStore is an in-memory DEKStore for tests. Lets the credentials
// service exercise its real ensure→encrypt→decrypt flow without spinning
// up a database.
type fakeStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newFakeStore() *fakeStore {
	return &fakeStore{data: map[string][]byte{}}
}

func (s *fakeStore) LoadDEK(_ context.Context, orgUID string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[orgUID]
	if !ok {
		return nil, false, nil
	}

	return v, true, nil
}

func (s *fakeStore) SaveDEK(_ context.Context, orgUID string, wrapped []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[orgUID] = wrapped

	return nil
}

func newKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	_, err := io.ReadFull(rand.Reader, key)
	require.NoError(t, err)

	return key
}

func TestServiceDisabledWhenNoMasterKey(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, err := credentials.NewService(nil, newFakeStore())
	r.NoError(err)
	r.False(svc.Enabled())

	_, err = svc.EncryptForOrg(t.Context(), "org-1", map[string]any{"password": "x"})
	r.ErrorIs(err, credentials.ErrDisabled)
}

func TestServiceRejectsWrongKeyLength(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, err := credentials.NewService([]byte("too-short"), newFakeStore())
	r.ErrorIs(err, credentials.ErrInvalidKey)
}

func TestServiceRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, err := credentials.NewService(newKey(t), newFakeStore())
	r.NoError(err)
	r.True(svc.Enabled())

	plain := map[string]any{"password": "hunter2", "token": "abc"}

	envelope, err := svc.EncryptForOrg(t.Context(), "org-1", plain)
	r.NoError(err)
	r.NotEmpty(envelope)

	out, err := svc.DecryptForOrg(t.Context(), "org-1", envelope)
	r.NoError(err)
	r.Equal(plain, out)
}

func TestServiceRejectsTampering(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, err := credentials.NewService(newKey(t), newFakeStore())
	r.NoError(err)

	envelope, err := svc.EncryptForOrg(t.Context(), "org-1", map[string]any{"password": "ok"})
	r.NoError(err)

	// Flip a byte inside the ciphertext field. The envelope has a numeric
	// `v` field, so unmarshal into map[string]any and treat ct as string.
	var env map[string]any
	r.NoError(json.Unmarshal([]byte(envelope), &env))
	ct, _ := env["ct"].(string)
	r.NotEmpty(ct)
	ctBytes := []byte(ct)
	ctBytes[0] ^= 0x01
	env["ct"] = string(ctBytes)
	tampered, mErr := json.Marshal(env)
	r.NoError(mErr)

	_, err = svc.DecryptForOrg(t.Context(), "org-1", string(tampered))
	r.Error(err, "GCM auth tag must reject tampered ciphertext")
}

func TestServiceCrossOrgIsolation(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, err := credentials.NewService(newKey(t), newFakeStore())
	r.NoError(err)

	envelope, err := svc.EncryptForOrg(t.Context(), "org-A", map[string]any{"password": "secret"})
	r.NoError(err)

	// Decrypting with a different orgUID uses a different DEK and must fail.
	_, err = svc.DecryptForOrg(t.Context(), "org-B", envelope)
	r.Error(err)
}

func TestSplitMergeRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	full := map[string]any{
		"url":      "https://example.com",
		"username": "alice",
		"password": "hunter2",
	}

	public, private := credentials.SplitConfig(full, []string{"password"})
	r.NotContains(public, "password")
	r.Contains(private, "password")
	r.Equal("https://example.com", public["url"])

	merged := credentials.MergeConfig(public, private)
	r.Equal(full, merged)
}

func TestSplitConfigEmptySecrets(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	full := map[string]any{"url": "x"}

	public, private := credentials.SplitConfig(full, nil)
	r.Equal(full, public)
	r.Empty(private)
}

func TestConnectionSecretFieldsRegistry(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Contains(credentials.ConnectionSecretFields("slack"), "access_token")
	r.Contains(credentials.ConnectionSecretFields("pagerduty"), "routing_key")
	r.Empty(credentials.ConnectionSecretFields("not-a-real-type"))
}

// TestConnectionSecretFieldsURLNotSecret guards the webhook-URL fix: endpoint
// URLs must stay in public settings so the dashboard edit form can render
// them. The webhook `url` and the discord/googlechat/mattermost/msteams
// `webhook_url` must NOT be reported as secret.
func TestConnectionSecretFieldsURLNotSecret(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.NotContains(credentials.ConnectionSecretFields("webhook"), "url")
	// Webhook still keeps its real secrets.
	r.Contains(credentials.ConnectionSecretFields("webhook"), "signingSecret")
	r.Contains(credentials.ConnectionSecretFields("webhook"), "auth_token")

	for _, ct := range []models.ConnectionType{"discord", "googlechat", "mattermost", "msteams"} {
		r.NotContains(credentials.ConnectionSecretFields(ct), "webhook_url",
			"%s webhook_url must not be a secret", ct)
	}
}

func TestProviderSecretFieldsRegistry(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Contains(credentials.ProviderSecretFields("google"), "client_secret")
	r.Empty(credentials.ProviderSecretFields("not-a-real-type"))
}
