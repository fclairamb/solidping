package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func newAppSettingsTestService(t *testing.T) *Service {
	t.Helper()

	svc, err := New(t.Context(), Config{InMemory: true})
	require.NoError(t, err)

	require.NoError(t, svc.Initialize(t.Context()))

	t.Cleanup(func() { _ = svc.Close() })

	return svc
}

func TestGetAppSetting_MissingKey(t *testing.T) {
	t.Parallel()

	svc := newAppSettingsTestService(t)
	ctx := context.Background()

	_, err := svc.GetAppSetting(ctx, "nonexistent.key")
	require.Error(t, err)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestSetAppSetting_Upsert(t *testing.T) {
	t.Parallel()

	svc := newAppSettingsTestService(t)
	ctx := context.Background()
	key := "test.key"

	require.NoError(t, svc.SetAppSetting(ctx, key, "first"))

	val, err := svc.GetAppSetting(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "first", val)

	require.NoError(t, svc.SetAppSetting(ctx, key, "second"))

	val, err = svc.GetAppSetting(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "second", val)
}
