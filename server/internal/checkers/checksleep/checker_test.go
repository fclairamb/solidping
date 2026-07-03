package checksleep_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checksleep"
)

func TestSleepChecker_Type(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &checksleep.SleepChecker{}
	r.Equal(checkerdef.CheckTypeSleep, checker.Type())
}

func TestSleepConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		config     map[string]any
		wantErr    bool
		errSubstr  string
		checkExtra func(r *require.Assertions, spec *checkerdef.CheckSpec)
	}{
		{
			name:      "missing sleep_ms",
			config:    map[string]any{},
			wantErr:   true,
			errSubstr: "sleep_ms",
		},
		{
			name:      "zero sleep_ms",
			config:    map[string]any{"sleep_ms": 0},
			wantErr:   true,
			errSubstr: "sleep_ms",
		},
		{
			name:      "negative sleep_ms",
			config:    map[string]any{"sleep_ms": -5},
			wantErr:   true,
			errSubstr: "sleep_ms",
		},
		{
			name:      "above cap",
			config:    map[string]any{"sleep_ms": 120001},
			wantErr:   true,
			errSubstr: "sleep_ms",
		},
		{
			name:      "jitter >= sleep",
			config:    map[string]any{"sleep_ms": 100, "jitter_ms": 100},
			wantErr:   true,
			errSubstr: "jitter_ms",
		},
		{
			name:      "negative jitter",
			config:    map[string]any{"sleep_ms": 100, "jitter_ms": -1},
			wantErr:   true,
			errSubstr: "jitter_ms",
		},
		{
			name:      "bad status",
			config:    map[string]any{"sleep_ms": 100, "status": "weird"},
			wantErr:   true,
			errSubstr: "status",
		},
		{
			name:    "valid minimal, name+slug autofilled",
			config:  map[string]any{"sleep_ms": 500},
			wantErr: false,
			checkExtra: func(r *require.Assertions, spec *checkerdef.CheckSpec) {
				r.Equal("sleep-500ms", spec.Name)
				r.Equal("sleep-500ms", spec.Slug)
			},
		},
		{
			name:    "valid with jitter and status",
			config:  map[string]any{"sleep_ms": 1000, "jitter_ms": 100, "status": "down"},
			wantErr: false,
		},
		{
			name:    "at cap",
			config:  map[string]any{"sleep_ms": 120000},
			wantErr: false,
		},
		{
			name:    "float sleep_ms from JSON",
			config:  map[string]any{"sleep_ms": 300.0},
			wantErr: false,
			checkExtra: func(r *require.Assertions, spec *checkerdef.CheckSpec) {
				r.Equal("sleep-300ms", spec.Name)
			},
		},
	}

	checker := &checksleep.SleepChecker{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			spec := &checkerdef.CheckSpec{Config: tt.config}
			err := checker.Validate(spec)

			if tt.wantErr {
				r.Error(err)

				if tt.errSubstr != "" {
					r.Contains(err.Error(), tt.errSubstr)
				}

				return
			}

			r.NoError(err)

			if tt.checkExtra != nil {
				tt.checkExtra(r, spec)
			}
		})
	}
}

// mustConfig builds a validated SleepConfig via the registry's config path.
func mustConfig(r *require.Assertions, m map[string]any) checkerdef.Config {
	cfg := &checksleep.SleepConfig{}
	r.NoError(cfg.FromMap(m))

	return cfg
}

func TestSleepChecker_Execute_Up(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &checksleep.SleepChecker{}
	cfg := mustConfig(r, map[string]any{"sleep_ms": 50, "jitter_ms": 0})

	res, err := checker.Execute(context.Background(), cfg)
	r.NoError(err)
	r.NotNil(res)
	r.Equal(checkerdef.StatusUp, res.Status)
	// Duration should be close to the requested 50ms (deterministic, no jitter).
	r.GreaterOrEqual(res.Duration, 45*time.Millisecond)
	r.Less(res.Duration, 500*time.Millisecond)
	// sleep_ms metric present.
	r.Contains(res.Metrics, "sleep_ms")
	r.Equal(int64(50), res.Metrics["sleep_ms"])
}

func TestSleepChecker_Execute_ForcedDown(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &checksleep.SleepChecker{}
	cfg := mustConfig(r, map[string]any{"sleep_ms": 30, "status": "down"})

	res, err := checker.Execute(context.Background(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusDown, res.Status)
}

func TestSleepChecker_Execute_ForcedError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &checksleep.SleepChecker{}
	cfg := mustConfig(r, map[string]any{"sleep_ms": 30, "status": "error"})

	res, err := checker.Execute(context.Background(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusError, res.Status)
}

// TestSleepChecker_Execute_ContextTimeout proves the checker honors ctx: a
// context timeout shorter than sleep_ms interrupts the sleep and yields
// StatusTimeout (spec D2 — the cost-aware timeout can interrupt).
func TestSleepChecker_Execute_ContextTimeout(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &checksleep.SleepChecker{}
	// 5s configured sleep, but ctx cancels after 20ms — never actually waits 5s.
	cfg := mustConfig(r, map[string]any{"sleep_ms": 5000, "jitter_ms": 0})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	res, err := checker.Execute(ctx, cfg)
	elapsed := time.Since(start)

	r.NoError(err)
	r.Equal(checkerdef.StatusTimeout, res.Status)
	// Returned promptly (well under the 5s configured sleep).
	r.Less(elapsed, 1*time.Second)
	r.Contains(res.Output, checkerdef.OutputKeyError)
}

func TestSleepChecker_Execute_DeterministicDuration(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &checksleep.SleepChecker{}
	cfg := mustConfig(r, map[string]any{"sleep_ms": 40, "jitter_ms": 0})

	res, err := checker.Execute(context.Background(), cfg)
	r.NoError(err)
	// With jitter_ms:0 the sleep target is exactly 40ms.
	r.Equal(int64(40), res.Metrics["sleep_ms"])
}

func TestSleepChecker_GetSampleConfigs(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &checksleep.SleepChecker{}
	samples := checker.GetSampleConfigs(nil)
	r.Len(samples, 2)

	// Each sample must validate.
	for i := range samples {
		spec := &checkerdef.CheckSpec{
			Name:   samples[i].Name,
			Slug:   samples[i].Slug,
			Config: samples[i].Config,
		}
		r.NoError(checker.Validate(spec))
	}
}
