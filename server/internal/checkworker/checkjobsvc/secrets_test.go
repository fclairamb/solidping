package checkjobsvc_test

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// memDEKStore is an in-memory credentials.DEKStore: the merge helper's
// contract is about the envelope, not about where the wrapped DEK is
// persisted, so these unit tests skip the database entirely.
type memDEKStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemDEKStore() *memDEKStore {
	return &memDEKStore{data: map[string][]byte{}}
}

func (s *memDEKStore) LoadDEK(_ context.Context, orgUID string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.data[orgUID]

	return v, ok, nil
}

func (s *memDEKStore) SaveDEK(_ context.Context, orgUID string, wrapped []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[orgUID] = wrapped

	return nil
}

// newEnabledCreds builds a credentials service with a fresh random master key.
func newEnabledCreds(t *testing.T) credentials.Service {
	t.Helper()

	key := make([]byte, 32)
	_, err := io.ReadFull(rand.Reader, key)
	require.NoError(t, err)

	svc, err := credentials.NewService(key, newMemDEKStore())
	require.NoError(t, err)

	return svc
}

// newDisabledCreds builds a credentials service with no master key — the
// documented self-hosted fallback where secrets stay plaintext.
func newDisabledCreds(t *testing.T) credentials.Service {
	t.Helper()

	svc, err := credentials.NewService(nil, newMemDEKStore())
	require.NoError(t, err)

	return svc
}

const secretsTestOrg = "org-secrets"

// TestMergeJobSecretsNoopWithoutEnvelope pins the encryption-disabled
// fallback: a job with no config_private is passed through byte-for-byte, so
// plaintext secrets living in the public config keep working.
func TestMergeJobSecretsNoopWithoutEnvelope(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	job := &models.CheckJob{
		OrganizationUID: secretsTestOrg,
		Config:          models.JSONMap{"url": "https://x.test", "password": "plaintext-pass"},
	}

	outcome, err := checkjobsvc.MergeJobSecrets(t.Context(), newDisabledCreds(t), job)
	r.NoError(err)
	r.Equal(checkjobsvc.SecretMergeNoop, outcome)
	r.Equal("plaintext-pass", job.Config["password"])
	r.Equal("https://x.test", job.Config["url"])
}

// TestMergeJobSecretsNoopWithNilService covers a worker built without any
// credentials service at all: same plaintext fallback, no panic.
func TestMergeJobSecretsNoopWithNilService(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	job := &models.CheckJob{
		OrganizationUID: secretsTestOrg,
		Config:          models.JSONMap{"url": "https://x.test"},
	}

	outcome, err := checkjobsvc.MergeJobSecrets(t.Context(), nil, job)
	r.NoError(err)
	r.Equal(checkjobsvc.SecretMergeNoop, outcome)
}

// TestMergeJobSecretsMergesAndStrips is the core contract: the private half
// lands in Config and the envelope is gone afterwards.
func TestMergeJobSecretsMergesAndStrips(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	creds := newEnabledCreds(t)

	envelope, err := creds.EncryptForOrg(ctx, secretsTestOrg, map[string]any{"password": "s3cr3t"})
	r.NoError(err)

	keys := `["password"]`
	job := &models.CheckJob{
		OrganizationUID:   secretsTestOrg,
		Config:            models.JSONMap{"url": "https://x.test"},
		ConfigPrivate:     &envelope,
		ConfigPrivateKeys: &keys,
		Encrypted:         true,
	}

	outcome, err := checkjobsvc.MergeJobSecrets(ctx, creds, job)
	r.NoError(err)
	r.Equal(checkjobsvc.SecretMergeMerged, outcome)
	r.Equal("s3cr3t", job.Config["password"])
	r.Equal("https://x.test", job.Config["url"])
	r.Nil(job.ConfigPrivate, "the envelope must be stripped after merge")
	r.Nil(job.ConfigPrivateKeys)
	r.False(job.Encrypted)
}

// TestMergeJobSecretsOpensPlaintextEnvelopeWithoutKey is the positive control
// for the no-master-key path: a plaintext envelope (the structural-separation
// fallback that keeps secrets out of the public config) is opened and merged
// into Config by a DISABLED credentials service, so a self-hosted worker with
// no master key keeps executing checks that carry a credential.
func TestMergeJobSecretsOpensPlaintextEnvelopeWithoutKey(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	envelope, err := credentials.SealPlaintext(map[string]any{"private_key": "SSH-KEY"})
	r.NoError(err)

	keys := `["private_key"]`
	job := &models.CheckJob{
		OrganizationUID:   secretsTestOrg,
		Config:            models.JSONMap{"host": "lan.test"},
		ConfigPrivate:     &envelope,
		ConfigPrivateKeys: &keys,
		Encrypted:         false,
	}

	outcome, err := checkjobsvc.MergeJobSecrets(t.Context(), newDisabledCreds(t), job)
	r.NoError(err)
	r.Equal(checkjobsvc.SecretMergeMerged, outcome)
	r.Equal("SSH-KEY", job.Config["private_key"], "the check must execute with its credential")
	r.Equal("lan.test", job.Config["host"])
	r.Nil(job.ConfigPrivate, "the envelope must be stripped after merge")
	r.Nil(job.ConfigPrivateKeys)
}

// TestMergeJobSecretsOpensPlaintextEnvelopeWithNilService covers the same path
// for a worker built with no credentials service at all — no panic, still
// merges.
func TestMergeJobSecretsOpensPlaintextEnvelopeWithNilService(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	envelope, err := credentials.SealPlaintext(map[string]any{"password": "pw"})
	r.NoError(err)

	job := &models.CheckJob{
		OrganizationUID: secretsTestOrg,
		Config:          models.JSONMap{"host": "db.test"},
		ConfigPrivate:   &envelope,
	}

	outcome, err := checkjobsvc.MergeJobSecrets(t.Context(), nil, job)
	r.NoError(err)
	r.Equal(checkjobsvc.SecretMergeMerged, outcome)
	r.Equal("pw", job.Config["password"])
}

// TestMergeJobSecretsPrivateWinsOnConflict pins MergeConfig's precedence: the
// encrypted side is the source of truth (the public side may hold a stale
// placeholder).
func TestMergeJobSecretsPrivateWinsOnConflict(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	creds := newEnabledCreds(t)

	envelope, err := creds.EncryptForOrg(ctx, secretsTestOrg, map[string]any{"password": "real"})
	r.NoError(err)

	job := &models.CheckJob{
		OrganizationUID: secretsTestOrg,
		Config:          models.JSONMap{"password": "stale"},
		ConfigPrivate:   &envelope,
	}

	outcome, err := checkjobsvc.MergeJobSecrets(ctx, creds, job)
	r.NoError(err)
	r.Equal(checkjobsvc.SecretMergeMerged, outcome)
	r.Equal("real", job.Config["password"])
}

// TestMergeJobSecretsUnavailableWithoutMasterKey covers the envelope-exists /
// no-key case: reported, never silently merged as-is.
func TestMergeJobSecretsUnavailableWithoutMasterKey(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	envelope := `{"v":1,"alg":"AES-256-GCM","nonce":"","ct":""}`
	job := &models.CheckJob{
		OrganizationUID: secretsTestOrg,
		Config:          models.JSONMap{"url": "https://x.test"},
		ConfigPrivate:   &envelope,
		Encrypted:       true,
	}

	outcome, err := checkjobsvc.MergeJobSecrets(t.Context(), newDisabledCreds(t), job)
	r.Equal(checkjobsvc.SecretMergeUnavailable, outcome)
	r.ErrorIs(err, checkjobsvc.ErrSecretsUnavailable)
	r.NotNil(job.ConfigPrivate, "a job that could not be merged must not be silently stripped")
}

// TestMergeJobSecretsFailedOnBadEnvelope covers a key-configured decrypt
// failure (tampered/foreign ciphertext).
func TestMergeJobSecretsFailedOnBadEnvelope(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	// Encrypt under one service, decrypt under another (different master key).
	envelope, err := newEnabledCreds(t).EncryptForOrg(ctx, secretsTestOrg, map[string]any{"password": "s3cr3t"})
	r.NoError(err)

	job := &models.CheckJob{
		OrganizationUID: secretsTestOrg,
		Config:          models.JSONMap{"url": "https://x.test"},
		ConfigPrivate:   &envelope,
		Encrypted:       true,
	}

	outcome, err := checkjobsvc.MergeJobSecrets(ctx, newEnabledCreds(t), job)
	r.Equal(checkjobsvc.SecretMergeFailed, outcome)
	r.ErrorIs(err, checkjobsvc.ErrSecretsUndecryptable)
	r.Nil(job.Config["password"], "no secret may leak out of a failed decrypt")
}

// TestMergeJobSecretsNamesTheOrgKeyLayer pins the failure taxonomy the incident
// asked for: when the ORG key itself cannot be opened — a worker holding the
// wrong master key, or an unreadable DEK row — the reason must say so instead
// of telling the operator to re-save the check, which cannot possibly help
// because saving needs the same key.
func TestMergeJobSecretsNamesTheOrgKeyLayer(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	// One shared DEK row, two different master keys: the reader can load the
	// org's DEK envelope but cannot unwrap it.
	store := newMemDEKStore()

	writerKey := make([]byte, 32)
	_, err := io.ReadFull(rand.Reader, writerKey)
	r.NoError(err)

	writer, err := credentials.NewService(writerKey, store)
	r.NoError(err)

	envelope, err := writer.EncryptForOrg(ctx, secretsTestOrg, map[string]any{"password": "s3cr3t"})
	r.NoError(err)

	readerKey := make([]byte, 32)
	_, err = io.ReadFull(rand.Reader, readerKey)
	r.NoError(err)

	reader, err := credentials.NewService(readerKey, store)
	r.NoError(err)

	job := &models.CheckJob{
		OrganizationUID: secretsTestOrg,
		Config:          models.JSONMap{"url": "https://x.test"},
		ConfigPrivate:   &envelope,
		Encrypted:       true,
	}

	outcome, err := checkjobsvc.MergeJobSecrets(ctx, reader, job)
	r.Equal(checkjobsvc.SecretMergeFailed, outcome)
	r.ErrorIs(err, checkjobsvc.ErrSecretsOrgKeyUndecryptable)
	r.NotErrorIs(err, checkjobsvc.ErrSecretsUndecryptable,
		"an org-key failure must not carry the re-save-the-check advice")
	r.Equal(checkjobsvc.ErrSecretsOrgKeyUndecryptable, checkjobsvc.ResultReason(outcome, err))
	r.Nil(job.Config["password"], "no secret may leak out of a failed decrypt")

	// The other two branches keep their own reasons.
	r.Equal(checkjobsvc.ErrSecretsUnavailable,
		checkjobsvc.ResultReason(checkjobsvc.SecretMergeUnavailable, err))
	r.Equal(checkjobsvc.ErrSecretsUndecryptable,
		checkjobsvc.ResultReason(checkjobsvc.SecretMergeFailed, errors.New("bad ciphertext")))
}
