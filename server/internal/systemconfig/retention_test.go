package systemconfig

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// fakeParameterReader serves canned global parameters, and can fail, so the
// precedence rules can be exercised without a database.
type fakeParameterReader struct {
	values map[string]any
	err    error
}

func (f fakeParameterReader) GetSystemParameter(
	_ context.Context, key string,
) (*models.Parameter, error) {
	if f.err != nil {
		return nil, f.err
	}

	value, ok := f.values[key]
	if !ok {
		return nil, nil //nolint:nilnil // "no such parameter" is (nil, nil) in db.Service
	}

	return &models.Parameter{Key: key, Value: models.JSONMap{"value": value}}, nil
}

func rawKey() string  { return string(KeyPerfAggRetentionRawHours) }
func hourKey() string { return string(KeyPerfAggRetentionHourDays) }
func dayKey() string  { return string(KeyPerfAggRetentionDayMonths) }

// TestResolveAggregationRetention_Precedence pins the documented order —
// env > global DB parameter > legacy koanf field > documented default — tier by
// tier. This is the contract the whole read-side clamp rests on: if the reader
// resolved a shorter retention than the aggregation job actually applies, it
// would clamp away raw rows that no rollup covers yet.
//
//nolint:paralleltest // t.Setenv forbids t.Parallel
func TestResolveAggregationRetention_Precedence(t *testing.T) {
	r := require.New(t)
	ctx := t.Context()

	// 1. Nothing configured anywhere → documented defaults.
	raw, hour, day := ResolveAggregationRetention(ctx, fakeParameterReader{}, nil)
	r.Equal(DefaultRetentionRawHours, raw)
	r.Equal(DefaultRetentionHourDays, hour)
	r.Equal(DefaultRetentionDayMonths, day)
	r.Equal(24, DefaultRetentionRawHours, "the default triple is a user-facing contract")
	r.Equal(7, DefaultRetentionHourDays)
	r.Equal(2, DefaultRetentionDayMonths)

	// 2. Legacy koanf field beats the default.
	cfg := &config.Config{}
	cfg.Aggregation.RetentionRaw = 48
	cfg.Aggregation.RetentionHour = 14
	cfg.Aggregation.RetentionDay = 6

	raw, hour, day = ResolveAggregationRetention(ctx, fakeParameterReader{}, cfg)
	r.Equal(48, raw)
	r.Equal(14, hour)
	r.Equal(6, day)

	// 3. The global performance.* parameter — what the "Aggregation" settings tab
	//    writes — beats the legacy koanf field. This is the case that made the
	//    read side and the job disagree before this change.
	db := fakeParameterReader{values: map[string]any{rawKey(): 168.0, hourKey(): 30.0}}

	raw, hour, day = ResolveAggregationRetention(ctx, db, cfg)
	r.Equal(168, raw, "the DB parameter must win over the koanf field")
	r.Equal(30, hour)
	r.Equal(6, day, "an unset tier still falls through to its koanf value")

	// 4. The env override beats the DB parameter.
	t.Setenv(EnvRetentionRawHours, "72")

	raw, _, _ = ResolveAggregationRetention(ctx, db, cfg)
	r.Equal(72, raw)
}

// TestResolveAggregationRetention_ValidationAndCoercion covers the value
// handling: a below-floor or unparseable value falls through instead of being
// applied, and the stored JSON number is coerced whatever concrete type it
// arrives as.
//
//nolint:paralleltest // t.Setenv forbids t.Parallel
func TestResolveAggregationRetention_ValidationAndCoercion(t *testing.T) {
	r := require.New(t)
	ctx := t.Context()

	// A parameter below the floor (< 1) must fall through to the default, not
	// clamp the raw query to zero hours.
	raw, _, _ := ResolveAggregationRetention(ctx,
		fakeParameterReader{values: map[string]any{rawKey(): 0.0}}, nil)
	r.Equal(DefaultRetentionRawHours, raw)

	raw, _, _ = ResolveAggregationRetention(ctx,
		fakeParameterReader{values: map[string]any{rawKey(): -5.0}}, nil)
	r.Equal(DefaultRetentionRawHours, raw)

	// A non-numeric value falls through too.
	raw, _, _ = ResolveAggregationRetention(ctx,
		fakeParameterReader{values: map[string]any{rawKey(): "not-a-number"}}, nil)
	r.Equal(DefaultRetentionRawHours, raw)

	// Numeric coercion: JSON decodes to float64, but int / int64 / numeric string
	// all appear depending on the write path.
	for name, value := range map[string]any{
		"float64": float64(96),
		"int":     96,
		"int64":   int64(96),
		"string":  " 96 ",
	} {
		got, _, _ := ResolveAggregationRetention(ctx,
			fakeParameterReader{values: map[string]any{rawKey(): value}}, nil)
		r.Equal(96, got, "stored as %s", name)
	}

	// An invalid env override falls through to the DB parameter rather than
	// winning with a broken value.
	t.Setenv(EnvRetentionRawHours, "0")

	raw, _, _ = ResolveAggregationRetention(ctx,
		fakeParameterReader{values: map[string]any{rawKey(): 120.0}}, nil)
	r.Equal(120, raw)

	// A read error is not fatal: the resolver falls through to koanf/default.
	cfg := &config.Config{}
	cfg.Aggregation.RetentionHour = 9

	_, hour, _ := ResolveAggregationRetention(ctx,
		fakeParameterReader{err: errors.New("db down")}, cfg)
	r.Equal(9, hour)
}

// TestResolveReadSideRetention_MatchesFullResolver asserts the two-tier read-side
// helper (which exists to avoid a third, unused parameter lookup per request)
// resolves exactly like the full three-tier resolver.
func TestResolveReadSideRetention_MatchesFullResolver(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	cfg := &config.Config{}
	cfg.Aggregation.RetentionHour = 14

	db := fakeParameterReader{values: map[string]any{rawKey(): 168.0, dayKey(): 12.0}}

	wantRaw, wantHour, _ := ResolveAggregationRetention(ctx, db, cfg)
	gotRaw, gotHour := ResolveReadSideRetention(ctx, db, cfg)

	r.Equal(wantRaw, gotRaw)
	r.Equal(wantHour, gotHour)
	r.Equal(168, gotRaw)
	r.Equal(14, gotHour)
}
