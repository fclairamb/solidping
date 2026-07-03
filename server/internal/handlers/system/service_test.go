package system

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
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
				r.ErrorIs(err, ErrInvalidParameter)
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

// TestLaneLoad verifies the per-worker lane-load aggregation: sums of cost and
// delay EWMAs and the duty-cycle sum per lane, with the claim's region
// eligibility applied (NULL-region jobs count for every worker; region-scoped
// jobs count only for workers whose region has the job region as a prefix).
func TestLaneLoad(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx := context.Background()
	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	defer func() { _ = dbSvc.Close() }()

	org := models.NewOrganization("lane-load", "Lane Load Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	euRegion := "eu"
	seedJob := func(slug string, lane uint8, region *string, periodSec int, costMs, delayMs float64, enabled bool) {
		check := models.NewCheck(org.UID, slug, "http")
		check.Name = &slug
		check.Enabled = enabled
		r.NoError(dbSvc.CreateCheck(ctx, check))

		// CreateCheck auto-materializes the check_jobs row; shape that row
		// instead of inserting a second one.
		_, updateErr := dbSvc.DB().NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("lane = ?", lane).
			Set("region = ?", region).
			Set("period = ?", timeutils.Duration(time.Duration(periodSec)*time.Second)).
			Set("cost_ewma_ms = ?", costMs).
			Set("delay_ewma_ms = ?", delayMs).
			Where("check_uid = ?", check.UID).
			Exec(ctx)
		r.NoError(updateErr)
	}

	// Global fast job: 100ms cost over 10s → duty 1%.
	seedJob("fast-global", scheduling.LaneFast, nil, 10, 100, 50, true)
	// Global slow job: 10s cost over 10s → duty 100%.
	seedJob("slow-global", scheduling.LaneSlow, nil, 10, 10000, 140000, true)
	// EU-scoped slow job: only the EU worker sees it. 3s over 60s → duty 5%.
	seedJob("slow-eu", scheduling.LaneSlow, &euRegion, 60, 3000, 1000, true)
	// Disabled check: never counted.
	seedJob("slow-disabled", scheduling.LaneSlow, nil, 10, 9999, 9999, false)

	defaultWorker := models.NewWorker("w-default", "Default Worker")
	defaultRegion := "default"
	defaultWorker.Region = &defaultRegion
	_, err = dbSvc.DB().NewInsert().Model(defaultWorker).Exec(ctx)
	r.NoError(err)

	euWorker := models.NewWorker("w-eu", "EU Worker")
	euFr := "eu-fr-paris"
	euWorker.Region = &euFr
	_, err = dbSvc.DB().NewInsert().Model(euWorker).Exec(ctx)
	r.NoError(err)

	svc := NewService(dbSvc)
	report, err := svc.LaneLoad(ctx)
	r.NoError(err)
	r.Len(report, 2)

	byUID := map[string]WorkerLaneLoad{}
	for _, row := range report {
		byUID[row.WorkerUID] = row
	}

	def := byUID[defaultWorker.UID]
	r.Equal(1, def.Fast.Jobs)
	r.InDelta(100, def.Fast.CostEwmaSumMs, 0.001)
	r.InDelta(50, def.Fast.DelayEwmaSumMs, 0.001)
	r.InDelta(1, def.Fast.DutySumPct, 0.001)
	r.Equal(1, def.Slow.Jobs, "the eu-scoped job must not count for the default worker")
	r.InDelta(10000, def.Slow.CostEwmaSumMs, 0.001)
	r.InDelta(140000, def.Slow.DelayEwmaSumMs, 0.001)
	r.InDelta(100, def.Slow.DutySumPct, 0.001)

	eu := byUID[euWorker.UID]
	r.Equal(2, eu.Slow.Jobs, "eu worker sees the global and the eu-scoped slow jobs")
	r.InDelta(13000, eu.Slow.CostEwmaSumMs, 0.001)
	r.InDelta(141000, eu.Slow.DelayEwmaSumMs, 0.001)
	r.InDelta(105, eu.Slow.DutySumPct, 0.001)
}
