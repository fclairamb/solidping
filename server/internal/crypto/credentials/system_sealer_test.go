package credentials

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

var errDBDown = errors.New("db down")

// memSystemParams is an in-memory SystemParameterDB with the raw service's
// `{"value": …}` wrapping and GetOrCreate semantics.
type memSystemParams struct {
	mu   sync.Mutex
	rows map[string]models.JSONMap
	// beforeCreate runs once inside GetOrCreate, to simulate a racing replica.
	beforeCreate func(rows map[string]models.JSONMap)
	// getErr and createErr make the matching call fail.
	getErr, createErr error
}

func (m *memSystemParams) GetSystemParameter(_ context.Context, key string) (*models.Parameter, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.getErr != nil {
		return nil, m.getErr
	}

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

	if m.createErr != nil {
		return nil, false, m.createErr
	}

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

func TestNewSystemSealerRejectsBadKey(t *testing.T) {
	t.Parallel()

	_, err := NewSystemSealer([]byte("too-short"), &memSystemParams{rows: map[string]models.JSONMap{}})
	require.ErrorIs(t, err, ErrInvalidKey)
}

func TestSystemDEKStoreLoadErrors(t *testing.T) {
	t.Parallel()

	boom := errDBDown
	ctx := t.Context()

	t.Run("get fails", func(t *testing.T) {
		t.Parallel()

		store := NewSystemDEKStore(&memSystemParams{getErr: boom})
		_, _, err := store.LoadDEK(ctx, "")
		require.ErrorIs(t, err, boom)
	})

	t.Run("value cannot be marshaled", func(t *testing.T) {
		t.Parallel()

		store := NewSystemDEKStore(&memSystemParams{rows: map[string]models.JSONMap{
			SystemDEKParameterKey: {"value": func() {}},
		}})
		_, _, err := store.LoadDEK(ctx, "")
		require.ErrorContains(t, err, "marshal system dek param value")
	})

	t.Run("value has the wrong shape", func(t *testing.T) {
		t.Parallel()

		store := NewSystemDEKStore(&memSystemParams{rows: map[string]models.JSONMap{
			SystemDEKParameterKey: {"value": 42},
		}})
		_, _, err := store.LoadDEK(ctx, "")
		require.ErrorIs(t, err, ErrDEKParamShape)
	})

	t.Run("save fails", func(t *testing.T) {
		t.Parallel()

		store := NewSystemDEKStore(&memSystemParams{createErr: boom})
		require.ErrorIs(t, store.SaveDEK(ctx, "", []byte("wrapped")), boom)
	})
}

func TestSystemSealerSealPropagatesStoreErrors(t *testing.T) {
	t.Parallel()

	boom := errDBDown

	t.Run("load fails", func(t *testing.T) {
		t.Parallel()

		sealer, err := NewSystemSealer(testKEK(t), &memSystemParams{getErr: boom})
		require.NoError(t, err)

		_, err = sealer.Seal(t.Context(), map[string]any{"value": "x"})
		require.ErrorIs(t, err, boom)
	})

	t.Run("save fails", func(t *testing.T) {
		t.Parallel()

		sealer, err := NewSystemSealer(testKEK(t), &memSystemParams{
			rows: map[string]models.JSONMap{}, createErr: boom,
		})
		require.NoError(t, err)

		_, err = sealer.Seal(t.Context(), map[string]any{"value": "x"})
		require.ErrorIs(t, err, boom)
	})
}

func TestSystemSealerOpenPropagatesLoadError(t *testing.T) {
	t.Parallel()

	boom := errDBDown

	sealer, err := NewSystemSealer(testKEK(t), &memSystemParams{getErr: boom})
	require.NoError(t, err)

	_, err = sealer.Open(t.Context(), "{}")
	require.ErrorIs(t, err, boom)
	require.NotErrorIs(t, err, ErrSystemDEKMissing)
}

// foreignDEKRow returns a system DEK row wrapped by a KEK nobody here holds.
func foreignDEKRow(t *testing.T) models.JSONMap {
	t.Helper()

	foreignStore := &memSystemParams{rows: map[string]models.JSONMap{}}

	foreign, err := NewSystemSealer(testKEK(t), foreignStore)
	require.NoError(t, err)

	_, err = foreign.Seal(t.Context(), map[string]any{"value": "x"})
	require.NoError(t, err)

	return foreignStore.rows[SystemDEKParameterKey]
}

func TestSystemSealerSealRaceLostTwice(t *testing.T) {
	t.Parallel()

	foreign := foreignDEKRow(t)
	store := &memSystemParams{rows: map[string]models.JSONMap{}}
	// The racing replica's key lands after our load but cannot be opened with
	// our KEK, so the retry fails as well.
	store.beforeCreate = func(rows map[string]models.JSONMap) {
		rows[SystemDEKParameterKey] = foreign
	}

	sealer, err := NewSystemSealer(testKEK(t), store)
	require.NoError(t, err)

	_, err = sealer.Seal(t.Context(), map[string]any{"value": "x"})
	require.ErrorIs(t, err, ErrOrgKeyUnavailable)
	require.NotErrorIs(t, err, ErrDEKRoundTrip)
}

func TestSystemSealerOpenWithWrongKEK(t *testing.T) {
	t.Parallel()

	store := &memSystemParams{rows: map[string]models.JSONMap{SystemDEKParameterKey: foreignDEKRow(t)}}

	sealer, err := NewSystemSealer(testKEK(t), store)
	require.NoError(t, err)

	_, err = sealer.Open(t.Context(), "{}")
	require.ErrorIs(t, err, ErrOrgKeyUnavailable)
	require.NotErrorIs(t, err, ErrSystemDEKMissing)
}
