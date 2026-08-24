package watchdog_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/watchdog"
)

// TestLoadConfigDefaultsToDisabled: an instance that never wrote the parameter
// must not suddenly start mailing whoever happens to be user #1.
func TestLoadConfigDefaultsToDisabled(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	cfg, err := watchdog.LoadConfig(t.Context(), env.db)
	r.NoError(err)
	r.False(cfg.Enabled)
	r.Empty(cfg.Recipients)
	r.Equal(watchdog.DefaultInterval, cfg.Interval())
	r.Equal(watchdog.DefaultRenotifyAfter, cfg.RenotifyAfter())
	r.Equal(watchdog.SeverityWarning, cfg.Severity())
}

// TestLoadConfigFillsEveryThreshold: an operator should only have to write the
// two keys they care about, and get sane bars for everything else. A zero
// re-notify window or a zero blast-radius floor is a flood machine, never a
// deliberate configuration, so zero means "unset".
func TestLoadConfigFillsEveryThreshold(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)
	ctx := t.Context()

	r.NoError(env.db.SetSystemParameter(ctx, watchdog.ParamPlatformWatchdog, map[string]any{
		"enabled":    true,
		"recipients": []string{"user-1", "user-2"},
	}, false))

	cfg, err := watchdog.LoadConfig(ctx, env.db)
	r.NoError(err)
	r.True(cfg.Enabled)
	r.Equal([]string{"user-1", "user-2"}, cfg.Recipients)
	r.Equal(watchdog.DefaultDarkRegionMinOverdueJobs, cfg.DarkRegionMinOverdueJobs)
	r.Equal(watchdog.DefaultDarkRegionMinOverdueAge, cfg.DarkRegionMinOverdueAge())
	r.InEpsilon(watchdog.DefaultFleetDropPercent, cfg.FleetDropPercent, 0.0001)
	r.Equal(watchdog.DefaultFleetMinBaseline, cfg.FleetMinBaseline)
	r.Equal(watchdog.DefaultStaleIncidentMinAge, cfg.StaleIncidentMinAge())
}

// TestLoadConfigHonorsOverrides proves every bar is genuinely tunable —
// "tunable via config" is a spec requirement, not a comment.
func TestLoadConfigHonorsOverrides(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)
	ctx := t.Context()

	r.NoError(env.db.SetSystemParameter(ctx, watchdog.ParamPlatformWatchdog, map[string]any{
		"enabled":                       true,
		"minSeverity":                   "critical",
		"intervalMinutes":               15,
		"renotifyAfterMinutes":          60,
		"darkRegionMinOverdueJobs":      42,
		"darkRegionMinOverdueMinutes":   3,
		"fleetDropPercent":              25,
		"fleetMinBaseline":              10,
		"staleIncidentMinMinutes":       5,
		"staleIncidentPeriodMultiplier": 7,
	}, false))

	cfg, err := watchdog.LoadConfig(ctx, env.db)
	r.NoError(err)
	r.Equal(watchdog.SeverityCritical, cfg.Severity())
	r.Equal(15*time.Minute, cfg.Interval())
	r.Equal(time.Hour, cfg.RenotifyAfter())
	r.Equal(42, cfg.DarkRegionMinOverdueJobs)
	r.Equal(3*time.Minute, cfg.DarkRegionMinOverdueAge())
	r.InEpsilon(25.0, cfg.FleetDropPercent, 0.0001)
	r.Equal(10, cfg.FleetMinBaseline)
	r.Equal(5*time.Minute, cfg.StaleIncidentMinAge())
	r.Equal(7, cfg.StaleIncidentPeriodMultiplier)
}

// TestValidateParameter is the write-time guard: a value the watchdog could
// not decode would otherwise only surface as a failing hourly job on an
// alerting path nobody watches.
func TestValidateParameter(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		value   any
		wantErr error
	}{
		{"minimal valid", map[string]any{"enabled": true, "recipients": []string{"uid-1"}}, nil},
		{"empty object", map[string]any{}, nil},
		{"valid severity", map[string]any{"minSeverity": "critical"}, nil},
		{"blank recipient", map[string]any{"recipients": []string{"uid-1", "  "}}, watchdog.ErrInvalidRecipient},
		{"unknown severity", map[string]any{"minSeverity": "page-me"}, watchdog.ErrInvalidMinSeverity},
		{"recipients not a list", map[string]any{"recipients": "uid-1"}, watchdog.ErrInvalidParameterShape},
		{"not an object", "enabled", watchdog.ErrInvalidParameterShape},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := watchdog.ValidateParameter(testCase.value)
			if testCase.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, testCase.wantErr)
		})
	}
}
