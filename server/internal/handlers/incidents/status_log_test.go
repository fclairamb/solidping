package incidents

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestStatusChangeLog(t *testing.T) {
	t.Parallel()

	name := "Local Redis"
	slug := "redis-localhost"
	check := &models.Check{UID: "check-uid", Name: &name, Slug: &slug}

	refused := func(status models.ResultStatus) *models.Result {
		raw := int(status)

		return &models.Result{
			Status: &raw,
			Output: models.JSONMap{checkerdef.OutputKeyError: "connection refused"},
		}
	}

	upStatus := int(models.ResultStatusUp)
	upResult := &models.Result{Status: &upStatus}

	timeoutStatus := int(models.ResultStatusTimeout)
	timeoutResult := &models.Result{Status: &timeoutStatus}

	testCases := []struct {
		name     string
		check    *models.Check
		previous models.CheckStatus
		current  models.CheckStatus
		result   *models.Result
		logged   bool
		level    slog.Level
		msg      string
		attrs    []any
	}{
		{
			name: "no change logs nothing", check: check,
			previous: models.CheckStatusUp, current: models.CheckStatusUp, result: upResult,
		},
		{
			name: "up to down names the check and the reason", check: check,
			previous: models.CheckStatusUp, current: models.CheckStatusDown, result: refused(models.ResultStatusDown),
			logged: true, level: slog.LevelInfo, msg: "Check down",
			attrs: []any{"name", name, "slug", slug, "previous", "up", "error", "connection refused"},
		},
		{
			name: "down to up has no reason", check: check,
			previous: models.CheckStatusDown, current: models.CheckStatusUp, result: upResult,
			logged: true, level: slog.LevelInfo, msg: "Check up",
			attrs: []any{"name", name, "slug", slug, "previous", "down"},
		},
		{
			name: "brand-new check coming up is debug", check: check,
			previous: models.CheckStatusCreated, current: models.CheckStatusUp, result: upResult,
			logged: true, level: slog.LevelDebug, msg: "Check up",
			attrs: []any{"name", name, "slug", slug, "previous", "created"},
		},
		{
			name: "brand-new check failing is info", check: check,
			previous: models.CheckStatusCreated, current: models.CheckStatusDown, result: refused(models.ResultStatusError),
			logged: true, level: slog.LevelInfo, msg: "Check down",
			attrs: []any{"name", name, "slug", slug, "previous", "created", "error", "connection refused"},
		},
		{
			name: "entering validating is debug and carries the fallback reason", check: check,
			previous: models.CheckStatusUp, current: models.CheckStatusValidating, result: timeoutResult,
			logged: true, level: slog.LevelDebug, msg: "Check validating",
			attrs: []any{"name", name, "slug", slug, "previous", "up", "error", reasonTimeout},
		},
		{
			name: "brand-new check entering validating is debug", check: check,
			previous: models.CheckStatusCreated, current: models.CheckStatusValidating, result: refused(models.ResultStatusDown),
			logged: true, level: slog.LevelDebug, msg: "Check validating",
			attrs: []any{"name", name, "slug", slug, "previous", "created", "error", "connection refused"},
		},
		{
			name: "confirmed down after validating is info", check: check,
			previous: models.CheckStatusValidating, current: models.CheckStatusDown, result: refused(models.ResultStatusDown),
			logged: true, level: slog.LevelInfo, msg: "Check down",
			attrs: []any{"name", name, "slug", slug, "previous", "validating", "error", "connection refused"},
		},
		{
			name: "blip recovered inside validating is debug", check: check,
			previous: models.CheckStatusValidating, current: models.CheckStatusUp, result: upResult,
			logged: true, level: slog.LevelDebug, msg: "Check up",
			attrs: []any{"name", name, "slug", slug, "previous", "validating"},
		},
		{
			name: "warning carries the checker message", check: check,
			previous: models.CheckStatusUp, current: models.CheckStatusWarning, result: refused(models.ResultStatusWarning),
			logged: true, level: slog.LevelInfo, msg: "Check warning",
			attrs: []any{"name", name, "slug", slug, "previous", "up", "error", "connection refused"},
		},
		{
			name: "warning without a message", check: check,
			previous: models.CheckStatusUp, current: models.CheckStatusWarning, result: upResult,
			logged: true, level: slog.LevelInfo, msg: "Check warning",
			attrs: []any{"name", name, "slug", slug, "previous", "up"},
		},
		{
			name: "unnamed check falls back to its uid", check: &models.Check{UID: "check-uid"},
			previous: models.CheckStatusStale, current: models.CheckStatusUp, result: upResult,
			logged: true, level: slog.LevelInfo, msg: "Check up",
			attrs: []any{"name", "", "slug", "check-uid", "previous", "stale"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			level, msg, attrs, logged := statusChangeLog(tc.check, tc.previous, tc.current, tc.result)
			r.Equal(tc.logged, logged)

			if !tc.logged {
				return
			}

			r.Equal(tc.level, level)
			r.Equal(tc.msg, msg)
			r.Equal(tc.attrs, attrs)
		})
	}
}
