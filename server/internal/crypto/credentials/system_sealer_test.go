package credentials

import (
	"context"
	"crypto/rand"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// memSystemParams is an in-memory SystemParameterDB with the raw service's
// `{"value": …}` wrapping and GetOrCreate semantics.
type memSystemParams struct {
	mu   sync.Mutex
	rows map[string]models.JSONMap
	// beforeCreate runs once inside GetOrCreate, to simulate a racing replica.
	beforeCreate func(rows map[string]models.JSONMap)
}

func (m *memSystemParams) GetSystemParameter(_ context.Context, key string) (*models.Parameter, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	value, ok := m.rows[key]
	if !ok {
		return nil, nil //nolint:nilnil // mirrors the db.Service contract
	}

	return &models.Parameter{Key: key, Value: value}, nil
}

func (m *memSystemParams) GetOrCreateSystemParameter(
	_ context.Context, key string, value any, _ bool,
) (*models.Parameter, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.beforeCreate != nil {
		m.beforeCreate(m.rows)
		m.beforeCreate = nil
	}

	if existing, ok := m.rows[key]; ok {
		return &models.Parameter{Key: key, Value: existing}, false, nil
	}

	m.rows[key] = models.ParameterValue(value)

	return &models.Parameter{Key: key, Value: m.rows[key]}, true, nil
}

func testKEK(t *testing.T) []byte {
	t.Helper()

	kek := make([]byte, 32)
	_, err := rand.Read(kek)
	require.NoError(t, err)

	return kek
}

func TestSystemSealerRoundTrip(t *testing.T) {
	t.Parallel()

	store := &memSystemParams{rows: map[string]models.JSONMap{}}
	kek := testKEK(t)

	sealer, err := NewSystemSealer(kek, store)
	require.NoError(t, err)
	require.True(t, sealer.Enabled())

	envelope, err := sealer.Seal(t.Context(), map[string]any{"value": "acme-secret"})
	require.NoError(t, err)
	require.NotContains(t, envelope, "acme-secret")
	require.Contains(t, store.rows, SystemDEKParameterKey)

	// A fresh process (cold cache) opens it with the same KEK.
	other, err := NewSystemSealer(kek, store)
	require.NoError(t, err)

	plain, err := other.Open(t.Context(), envelope)
	require.NoError(t, err)
	require.Equal(t, "acme-secret", plain["value"])
}

func TestSystemSealerLostCreationRace(t *testing.T) {
	t.Parallel()

	kek := testKEK(t)
	store := &memSystemParams{rows: map[string]models.JSONMap{}}

	// The other replica creates its key between our load and our create.
	winnerStore := &memSystemParams{rows: map[string]models.JSONMap{}}

	winner, err := NewSystemSealer(kek, winnerStore)
	require.NoError(t, err)

	winnerEnvelope, err := winner.Seal(t.Context(), map[string]any{"value": "from-winner"})
	require.NoError(t, err)

	store.beforeCreate = func(rows map[string]models.JSONMap) {
		rows[SystemDEKParameterKey] = winnerStore.rows[SystemDEKParameterKey]
	}

	loser, err := NewSystemSealer(kek, store)
	require.NoError(t, err)

	envelope, err := loser.Seal(t.Context(), map[string]any{"value": "from-loser"})
	require.NoError(t, err)

	// Both replicas now share the winner's key.
	plain, err := winner.Open(t.Context(), envelope)
	require.NoError(t, err)
	require.Equal(t, "from-loser", plain["value"])

	plain, err = loser.Open(t.Context(), winnerEnvelope)
	require.NoError(t, err)
	require.Equal(t, "from-winner", plain["value"])
}

func TestSystemSealerOpenNeverGeneratesKey(t *testing.T) {
	t.Parallel()

	kek := testKEK(t)
	store := &memSystemParams{rows: map[string]models.JSONMap{}}

	sealer, err := NewSystemSealer(kek, store)
	require.NoError(t, err)

	envelope, err := sealer.Seal(t.Context(), map[string]any{"value": "x"})
	require.NoError(t, err)

	empty := &memSystemParams{rows: map[string]models.JSONMap{}}

	cold, err := NewSystemSealer(kek, empty)
	require.NoError(t, err)

	_, err = cold.Open(t.Context(), envelope)
	require.ErrorIs(t, err, ErrSystemDEKMissing)
	require.Empty(t, empty.rows, "opening must not create a system DEK")
}

func TestSystemSealerDisabled(t *testing.T) {
	t.Parallel()

	sealer, err := NewSystemSealer(nil, &memSystemParams{rows: map[string]models.JSONMap{}})
	require.NoError(t, err)
	require.False(t, sealer.Enabled())

	_, err = sealer.Seal(t.Context(), map[string]any{})
	require.ErrorIs(t, err, ErrDisabled)

	_, err = sealer.Open(t.Context(), "{}")
	require.ErrorIs(t, err, ErrDisabled)
}
