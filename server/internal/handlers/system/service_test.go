package system

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
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
