package systemconfig

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Documented aggregation-retention defaults, used when nothing is configured
// anywhere. Keep in sync with jobtypes' defaultRetention* constants — these are
// the user-facing contract (spec 2026-07-14-02).
const (
	DefaultRetentionRawHours  = 24
	DefaultRetentionHourDays  = 7
	DefaultRetentionDayMonths = 2
)

// Env vars that override the corresponding performance.* parameter.
const (
	EnvRetentionRawHours  = "SP_PERFORMANCE_AGGREGATION_RETENTION_RAW_HOURS"
	EnvRetentionHourDays  = "SP_PERFORMANCE_AGGREGATION_RETENTION_HOUR_DAYS"
	EnvRetentionDayMonths = "SP_PERFORMANCE_AGGREGATION_RETENTION_DAY_MONTHS"
)

// RetentionParameterReader is the minimal db surface needed to resolve the live
// retention parameters (db.Service satisfies it).
type RetentionParameterReader interface {
	GetSystemParameter(ctx context.Context, key string) (*models.Parameter, error)
}

// ResolveAggregationRetention resolves the per-tier aggregation retention
// (raw→hour hours, hour→day days, day→month months) with the precedence
// env > global DB parameter > legacy koanf field > documented default.
//
// This is the SINGLE source of truth for retention on both sides of the system:
// the aggregation job uses it to decide what to roll up and delete
// (jobtypes.retentionFromConfig delegates here), and the read side uses it to
// bound its raw-tier queries (uptimebar's raw clamp, via each service's
// uptimebarHints). The two must agree: if a reader assumed 24 h while the job
// actually keeps 168 h, the reader would clamp away six days of raw rows that
// have no rollup covering them yet. Reading the legacy koanf field alone is NOT
// enough — the server "Aggregation" settings tab writes the performance.*
// parameters, which never reach the koanf struct.
//
// db and cfg may both be nil (callers without either fall back to the defaults).
//
// Returns (rawHours, hourDays, dayMonths).
func ResolveAggregationRetention(
	ctx context.Context, db RetentionParameterReader, cfg *config.Config,
) (int, int, int) {
	legacyRaw, legacyHour, legacyDay := 0, 0, 0
	if cfg != nil {
		legacyRaw = cfg.Aggregation.RetentionRaw
		legacyHour = cfg.Aggregation.RetentionHour
		legacyDay = cfg.Aggregation.RetentionDay
	}

	rawHours := resolveRetentionTier(ctx, db,
		KeyPerfAggRetentionRawHours, EnvRetentionRawHours, legacyRaw, DefaultRetentionRawHours)
	hourDays := resolveRetentionTier(ctx, db,
		KeyPerfAggRetentionHourDays, EnvRetentionHourDays, legacyHour, DefaultRetentionHourDays)
	dayMonths := resolveRetentionTier(ctx, db,
		KeyPerfAggRetentionDayMonths, EnvRetentionDayMonths, legacyDay, DefaultRetentionDayMonths)

	return rawHours, hourDays, dayMonths
}

// ResolveReadSideRetention resolves only the two tiers the read side needs — raw
// hours and hourly days — with the same precedence as
// ResolveAggregationRetention. The day/month tier is deliberately not resolved:
// nothing on the read path uses it, and every tier costs one parameter lookup.
//
// Callers resolve this ONCE per request and thread the result down (see each
// service's uptimebarHints): this sits inside the request path of a spec whose
// whole purpose is removing per-request latency, and the availability endpoint
// computes several windows per request.
//
// Returns (rawHours, hourDays).
func ResolveReadSideRetention(
	ctx context.Context, db RetentionParameterReader, cfg *config.Config,
) (int, int) {
	legacyRaw, legacyHour := 0, 0
	if cfg != nil {
		legacyRaw = cfg.Aggregation.RetentionRaw
		legacyHour = cfg.Aggregation.RetentionHour
	}

	return resolveRetentionTier(ctx, db,
			KeyPerfAggRetentionRawHours, EnvRetentionRawHours, legacyRaw, DefaultRetentionRawHours),
		resolveRetentionTier(ctx, db,
			KeyPerfAggRetentionHourDays, EnvRetentionHourDays, legacyHour, DefaultRetentionHourDays)
}

// resolveRetentionTier applies the documented precedence for one tier.
func resolveRetentionTier(
	ctx context.Context, db RetentionParameterReader,
	key ParameterKey, envVar string, legacy, def int,
) int {
	// 1. Environment variable (highest precedence).
	if n, ok := retentionFromEnv(ctx, envVar); ok {
		return n
	}

	// 2. Global DB parameter (organization_uid IS NULL).
	if n, ok := retentionFromDBParam(ctx, db, key); ok {
		return n
	}

	// 3. Legacy koanf field (deprecated back-compat).
	//
	// This branch deliberately does NOT log the deprecation warning the
	// aggregation job used to emit here. The resolver is now also on the READ
	// path — every status-page render and every availability request — so a
	// per-resolution warning would go from one line per aggregation run to one
	// per HTTP request. The deprecation is documented on
	// config.AggregationConfig instead.
	if legacy >= 1 {
		return legacy
	}

	// 4. Hardcoded default.
	return def
}

// retentionFromEnv reads and validates an SP_PERFORMANCE_* override.
func retentionFromEnv(ctx context.Context, envVar string) (int, bool) {
	raw := strings.TrimSpace(os.Getenv(envVar))
	if raw == "" {
		return 0, false
	}

	if n, err := strconv.Atoi(raw); err == nil && n >= 1 {
		return n, true
	}

	slog.WarnContext(ctx, "Ignoring invalid retention env override, falling through",
		"env", envVar, "value", raw)

	return 0, false
}

// retentionFromDBParam reads and validates the global performance.* parameter.
func retentionFromDBParam(
	ctx context.Context, db RetentionParameterReader, key ParameterKey,
) (int, bool) {
	if db == nil {
		return 0, false
	}

	param, err := db.GetSystemParameter(ctx, string(key))
	if err != nil || param == nil {
		return 0, false
	}

	n, ok := retentionParameterInt(param)
	if !ok {
		return 0, false
	}

	if n >= 1 {
		return n, true
	}

	slog.WarnContext(ctx, "Ignoring invalid retention parameter, falling through",
		"key", string(key), "value", n)

	return 0, false
}

// retentionParameterInt coerces a stored parameter value to an int.
func retentionParameterInt(param *models.Parameter) (int, bool) {
	value, ok := param.Value["value"]
	if !ok {
		return 0, false
	}

	switch n := value.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return parsed, true
		}
	}

	return 0, false
}
