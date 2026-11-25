package sysparamseal_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sysparamseal"
)

var errBoom = errors.New("boom")

// fakeDB overrides the system parameter methods of an otherwise nil db.Service.
type fakeDB struct {
	db.Service

	params    []*models.Parameter
	listErr   error
	getErr    error
	setErr    error
	createErr error
	// created is what GetOrCreateSystemParameter returns (nil: echo the input).
	created *models.Parameter
	stored  map[string]any
}

func (f *fakeDB) GetSystemParameter(_ context.Context, key string) (*models.Parameter, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}

	for _, param := range f.params {
		if param.Key == key {
			return param, nil
		}
	}

	return nil, nil //nolint:nilnil // mirrors the db.Service contract
}

func (f *fakeDB) ListSystemParameters(context.Context) ([]*models.Parameter, error) {
	return f.params, f.listErr
}

func (f *fakeDB) SetSystemParameter(_ context.Context, key string, value any, _ bool) error {
	if f.setErr != nil {
		return f.setErr
	}

	f.stored = map[string]any{key: value}

	return nil
}

func (f *fakeDB) GetOrCreateSystemParameter(
	_ context.Context, key string, value any, secret bool,
) (*models.Parameter, bool, error) {
	if f.createErr != nil {
		return nil, false, f.createErr
	}

	if f.created != nil {
		return f.created, false, nil
	}

	return &models.Parameter{Key: key, Value: models.ParameterValue(value), Secret: &secret}, true, nil
}

func (f *fakeDB) DeleteSystemParameter(context.Context, string) error {
	return f.setErr
}

// fakeSealer seals to a fixed envelope and opens to a fixed value.
type fakeSealer struct {
	enabled bool
	sealErr error
	openErr error
	opened  map[string]any
}

func (f *fakeSealer) Enabled() bool { return f.enabled }

func (f *fakeSealer) Seal(context.Context, map[string]any) (string, error) {
	return "env", f.sealErr
}

func (f *fakeSealer) Open(context.Context, string) (map[string]any, error) {
	return f.opened, f.openErr
}

func boolPtr(b bool) *bool { return &b }

func sealedParam(key string) *models.Parameter {
	return &models.Parameter{
		Key:    key,
		Value:  models.ParameterValue(map[string]any{sysparamseal.SealedMarkerKey: "env"}),
		Secret: boolPtr(true),
	}
}

func plainSecret(key string) *models.Parameter {
	return &models.Parameter{Key: key, Value: models.ParameterValue("plain"), Secret: boolPtr(true)}
}

func TestWrapDefaultsAndAccessors(t *testing.T) {
	t.Parallel()

	inner := &fakeDB{}

	svc := sysparamseal.Wrap(inner, &fakeSealer{enabled: true, opened: models.ParameterValue("v")}, nil)
	require.True(t, svc.Enabled())
	require.Same(t, inner, svc.Raw())

	// The default predicate seals what is flagged secret and nothing else.
	_, _, err := svc.GetOrCreateSystemParameter(t.Context(), "k", "v", false)
	require.NoError(t, err)

	param, created, err := svc.GetOrCreateSystemParameter(t.Context(), "k", "v", true)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "v", param.Value[models.ParameterValueKey], "a created row is opened")

	require.False(t, sysparamseal.Wrap(inner, nil, nil).Enabled())
	require.False(t, sysparamseal.Wrap(inner, &fakeSealer{}, nil).Enabled())
}

func TestGetAndListPassErrorsThrough(t *testing.T) {
	t.Parallel()

	svc := sysparamseal.Wrap(&fakeDB{getErr: errBoom, listErr: errBoom}, &fakeSealer{enabled: true}, nil)

	_, err := svc.GetSystemParameter(t.Context(), "k")
	require.ErrorIs(t, err, errBoom)

	_, err = svc.ListSystemParameters(t.Context())
	require.ErrorIs(t, err, errBoom)

	missing, err := sysparamseal.Wrap(&fakeDB{}, &fakeSealer{enabled: true}, nil).GetSystemParameter(t.Context(), "k")
	require.NoError(t, err)
	require.Nil(t, missing)
}

func TestListOpensSealedRows(t *testing.T) {
	t.Parallel()

	inner := &fakeDB{params: []*models.Parameter{sealedParam("a"), plainSecret("b")}}
	sealer := &fakeSealer{enabled: true, opened: models.ParameterValue("opened")}

	params, err := sysparamseal.Wrap(inner, sealer, nil).ListSystemParameters(t.Context())
	require.NoError(t, err)
	require.Equal(t, "opened", params[0].Value[models.ParameterValueKey])
	require.Equal(t, "plain", params[1].Value[models.ParameterValueKey])
}

func TestListFailsOnUnopenableRow(t *testing.T) {
	t.Parallel()

	inner := &fakeDB{params: []*models.Parameter{sealedParam("a")}}

	_, err := sysparamseal.Wrap(inner, &fakeSealer{enabled: true, openErr: errBoom}, nil).
		ListSystemParameters(t.Context())
	require.ErrorIs(t, err, sysparamseal.ErrSealedUnavailable)
	require.ErrorIs(t, err, errBoom)

	_, err = sysparamseal.Wrap(inner, &fakeSealer{}, nil).ListSystemParameters(t.Context())
	require.ErrorIs(t, err, sysparamseal.ErrSealedUnavailable)
	require.ErrorContains(t, err, "SP_ENCRYPTION_MASTER_KEY")
}

func TestSetSystemParameter(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	t.Run("seals a secret", func(t *testing.T) {
		t.Parallel()

		inner := &fakeDB{}
		require.NoError(t, sysparamseal.Wrap(inner, &fakeSealer{enabled: true}, nil).
			SetSystemParameter(ctx, "k", "v", true))
		require.Equal(t, map[string]any{"k": map[string]any{sysparamseal.SealedMarkerKey: "env"}}, inner.stored)
	})

	t.Run("leaves a non secret", func(t *testing.T) {
		t.Parallel()

		inner := &fakeDB{}
		require.NoError(t, sysparamseal.Wrap(inner, &fakeSealer{enabled: true}, nil).
			SetSystemParameter(ctx, "k", "v", false))
		require.Equal(t, map[string]any{"k": "v"}, inner.stored)
	})

	t.Run("seal error", func(t *testing.T) {
		t.Parallel()

		err := sysparamseal.Wrap(&fakeDB{}, &fakeSealer{enabled: true, sealErr: errBoom}, nil).
			SetSystemParameter(ctx, "k", "v", true)
		require.ErrorIs(t, err, errBoom)
	})
}

func TestGetOrCreateSystemParameter(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	enabled := &fakeSealer{enabled: true, opened: models.ParameterValue("won")}

	t.Run("reserved key", func(t *testing.T) {
		t.Parallel()

		_, _, err := sysparamseal.Wrap(&fakeDB{}, enabled, nil).
			GetOrCreateSystemParameter(ctx, credentials.SystemDEKParameterKey, "v", true)
		require.ErrorIs(t, err, sysparamseal.ErrReservedKey)
	})

	t.Run("seal error", func(t *testing.T) {
		t.Parallel()

		_, _, err := sysparamseal.Wrap(&fakeDB{}, &fakeSealer{enabled: true, sealErr: errBoom}, nil).
			GetOrCreateSystemParameter(ctx, "k", "v", true)
		require.ErrorIs(t, err, errBoom)
	})

	t.Run("db error", func(t *testing.T) {
		t.Parallel()

		_, _, err := sysparamseal.Wrap(&fakeDB{createErr: errBoom}, enabled, nil).
			GetOrCreateSystemParameter(ctx, "k", "v", true)
		require.ErrorIs(t, err, errBoom)
	})

	t.Run("opens the winning row", func(t *testing.T) {
		t.Parallel()

		inner := &fakeDB{created: sealedParam("k")}

		param, created, err := sysparamseal.Wrap(inner, enabled, nil).GetOrCreateSystemParameter(ctx, "k", "v", true)
		require.NoError(t, err)
		require.False(t, created)
		require.Equal(t, "won", param.Value[models.ParameterValueKey])
	})

	t.Run("winning row cannot be opened", func(t *testing.T) {
		t.Parallel()

		inner := &fakeDB{created: sealedParam("k")}

		_, _, err := sysparamseal.Wrap(inner, &fakeSealer{enabled: true, openErr: errBoom}, nil).
			GetOrCreateSystemParameter(ctx, "k", "v", true)
		require.ErrorIs(t, err, sysparamseal.ErrSealedUnavailable)
	})
}

func TestDeleteSystemParameter(t *testing.T) {
	t.Parallel()

	svc := sysparamseal.Wrap(&fakeDB{setErr: errBoom}, &fakeSealer{enabled: true}, nil)

	require.ErrorIs(t, svc.DeleteSystemParameter(t.Context(), "k"), errBoom)
}

func TestSealPlaintextRowsErrors(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	rows := []*models.Parameter{sealedParam("done"), plainSecret("todo")}

	t.Run("disabled is a no-op", func(t *testing.T) {
		t.Parallel()

		n, err := sysparamseal.Wrap(&fakeDB{params: rows}, &fakeSealer{}, nil).SealPlaintextRows(ctx)
		require.NoError(t, err)
		require.Zero(t, n)
	})

	t.Run("list error", func(t *testing.T) {
		t.Parallel()

		_, err := sysparamseal.Wrap(&fakeDB{listErr: errBoom}, &fakeSealer{enabled: true}, nil).SealPlaintextRows(ctx)
		require.ErrorIs(t, err, errBoom)
	})

	t.Run("seal error", func(t *testing.T) {
		t.Parallel()

		n, err := sysparamseal.Wrap(&fakeDB{params: rows}, &fakeSealer{enabled: true, sealErr: errBoom}, nil).
			SealPlaintextRows(ctx)
		require.ErrorIs(t, err, errBoom)
		require.Zero(t, n)
	})

	t.Run("store error", func(t *testing.T) {
		t.Parallel()

		_, err := sysparamseal.Wrap(&fakeDB{params: rows, setErr: errBoom}, &fakeSealer{enabled: true}, nil).
			SealPlaintextRows(ctx)
		require.ErrorIs(t, err, errBoom)
	})

	t.Run("seals only the plaintext secret", func(t *testing.T) {
		t.Parallel()

		inner := &fakeDB{params: rows}

		n, err := sysparamseal.Wrap(inner, &fakeSealer{enabled: true}, nil).SealPlaintextRows(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, n)
		require.Contains(t, inner.stored, "todo")
	})
}

func TestPlaintextSecretKeys(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	_, err := sysparamseal.Wrap(&fakeDB{listErr: errBoom}, &fakeSealer{enabled: true}, nil).PlaintextSecretKeys(ctx)
	require.ErrorIs(t, err, errBoom)

	dek := plainSecret(credentials.SystemDEKParameterKey)
	inner := &fakeDB{params: []*models.Parameter{sealedParam("done"), plainSecret("todo"), dek, nil}}

	keys, err := sysparamseal.Wrap(inner, &fakeSealer{enabled: true}, nil).PlaintextSecretKeys(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"todo"}, keys)
}

func TestSealedEnvelope(t *testing.T) {
	t.Parallel()

	envelope, ok := sysparamseal.SealedEnvelope(models.ParameterValue(models.JSONMap{sysparamseal.SealedMarkerKey: "e"}))
	require.True(t, ok)
	require.Equal(t, "e", envelope)

	for name, value := range map[string]models.JSONMap{
		"not an object": models.ParameterValue("plain"),
		"empty marker":  models.ParameterValue(map[string]any{sysparamseal.SealedMarkerKey: ""}),
		"marker typed":  models.ParameterValue(map[string]any{sysparamseal.SealedMarkerKey: 3}),
		"no value":      {},
	} {
		_, ok := sysparamseal.SealedEnvelope(value)
		require.False(t, ok, name)
	}
}
