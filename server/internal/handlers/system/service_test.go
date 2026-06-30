package system

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

func TestToResponse_MasksSecrets(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc := &Service{}
	now := time.Now()
	secret := true

	param := &models.Parameter{
		UID:       "test-uid",
		Key:       "auth.jwt_secret",
		Value:     models.JSONMap{"value": "super-secret-value"},
		Secret:    &secret,
		UpdatedAt: now,
	}

	resp := svc.toResponse(param)

	r.Equal("auth.jwt_secret", resp.Key)
	r.Equal("******", resp.Value)
	r.True(resp.Secret)
	r.Equal(now, resp.UpdatedAt)
}

func TestToResponse_NonSecret(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc := &Service{}
	now := time.Now()
	secret := false

	param := &models.Parameter{
		UID:       "test-uid",
		Key:       "server.base_url",
		Value:     models.JSONMap{"value": "https://example.com"},
		Secret:    &secret,
		UpdatedAt: now,
	}

	resp := svc.toResponse(param)

	r.Equal("server.base_url", resp.Key)
	r.Equal("https://example.com", resp.Value)
	r.False(resp.Secret)
}

func TestExtractValue(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc := &Service{}

	// Test with value key
	val := svc.extractValue(models.JSONMap{"value": "test"})
	r.Equal("test", val)

	// Test with number value
	val = svc.extractValue(models.JSONMap{"value": 42.0})
	r.InEpsilon(42.0, val, 0.0001)

	// Test without value key - returns full map
	val = svc.extractValue(models.JSONMap{"other": "data"})
	mapVal, ok := val.(models.JSONMap)
	r.True(ok)
	r.Equal("data", mapVal["other"])
}

// TestSetParameterValidatesPasswordParams verifies that SetParameter rejects
// out-of-range auth.password.* values with ErrInvalidParameter (mapped to 422 by
// the handler) and persists valid ones. Non-password keys are not validated here.
func TestSetParameterValidatesPasswordParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     string
		value   any
		wantErr bool
	}{
		{name: "unknown algorithm rejected", key: "auth.password.algorithm", value: "sha1", wantErr: true},
		{name: "bcrypt cost too high rejected", key: "auth.password.bcrypt.cost", value: float64(99), wantErr: true},
		{name: "argon2 memory below floor rejected", key: "auth.password.argon2.memory", value: float64(1024), wantErr: true},
		{name: "rehash non-bool rejected", key: "auth.password.rehash_on_login", value: "soon", wantErr: true},
		{name: "valid argon2id accepted", key: "auth.password.algorithm", value: "argon2id", wantErr: false},
		{name: "valid 19 MiB memory accepted", key: "auth.password.argon2.memory", value: float64(19456), wantErr: false},
		{name: "valid time accepted", key: "auth.password.argon2.time", value: float64(2), wantErr: false},
		{name: "valid threads accepted", key: "auth.password.argon2.threads", value: float64(1), wantErr: false},
		{name: "valid bcrypt cost accepted", key: "auth.password.bcrypt.cost", value: float64(12), wantErr: false},
		{name: "valid rehash bool accepted", key: "auth.password.rehash_on_login", value: true, wantErr: false},
		// A non-password key is passed through untouched (no validation here).
		{name: "non-password key not validated", key: "server.base_url", value: "https://x", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			ctx := context.Background()

			dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
			r.NoError(err)
			r.NoError(dbSvc.Initialize(ctx))
			t.Cleanup(func() { _ = dbSvc.Close() })

			svc := NewService(dbSvc)
			resp, err := svc.SetParameter(ctx, tt.key, tt.value, false)

			if tt.wantErr {
				r.Error(err)
				r.True(errors.Is(err, ErrInvalidParameter),
					"expected ErrInvalidParameter, got %v", err)
				r.Nil(resp)

				// The bad value must NOT have been persisted.
				stored, getErr := dbSvc.GetSystemParameter(ctx, tt.key)
				r.NoError(getErr)
				r.Nil(stored, "rejected value must not be persisted")

				return
			}

			r.NoError(err)
			r.NotNil(resp)
			r.Equal(tt.key, resp.Key)
		})
	}
}

// MockDBService is a mock implementation of db.Service for testing.
type MockDBService struct {
	params map[string]*models.Parameter
}

func (m *MockDBService) GetSystemParameter(_ context.Context, key string) (*models.Parameter, error) {
	if p, ok := m.params[key]; ok {
		return p, nil
	}

	return nil, sql.ErrNoRows
}

func (m *MockDBService) ListSystemParameters(_ context.Context) ([]*models.Parameter, error) {
	result := make([]*models.Parameter, 0, len(m.params))
	for _, p := range m.params {
		result = append(result, p)
	}

	return result, nil
}

func (m *MockDBService) SetSystemParameter(_ context.Context, key string, value any, secret bool) error {
	now := time.Now()
	m.params[key] = &models.Parameter{
		UID:       key,
		Key:       key,
		Value:     models.JSONMap{"value": value},
		Secret:    &secret,
		CreatedAt: now,
		UpdatedAt: now,
	}

	return nil
}

func (m *MockDBService) DeleteSystemParameter(_ context.Context, key string) error {
	delete(m.params, key)

	return nil
}
