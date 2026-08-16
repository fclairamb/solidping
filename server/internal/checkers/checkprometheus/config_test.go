package checkprometheus_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkprometheus"
)

func f64(v float64) *float64 { return &v }

func TestPrometheusConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     checkprometheus.PrometheusConfig
		wantErr string
	}{
		{
			name: "minimal scrape",
			cfg: checkprometheus.PrometheusConfig{
				URL: "https://x/metrics", Metric: "m", CriticalValue: f64(10),
			},
		},
		{
			name: "minimal promql",
			cfg: checkprometheus.PrometheusConfig{
				URL: "https://x", Mode: checkprometheus.ModePromQL, Query: "up", CriticalValue: f64(1),
			},
		},
		{
			name:    "url required",
			cfg:     checkprometheus.PrometheusConfig{Metric: "m", CriticalValue: f64(1)},
			wantErr: "url",
		},
		{
			name: "url scheme",
			cfg: checkprometheus.PrometheusConfig{
				URL: "ftp://x", Metric: "m", CriticalValue: f64(1),
			},
			wantErr: "url",
		},
		{
			name: "unknown mode",
			cfg: checkprometheus.PrometheusConfig{
				URL: "https://x", Mode: "range", Metric: "m", CriticalValue: f64(1),
			},
			wantErr: "mode",
		},
		{
			name:    "metric required in scrape",
			cfg:     checkprometheus.PrometheusConfig{URL: "https://x", CriticalValue: f64(1)},
			wantErr: "metric",
		},
		{
			name: "query required in promql",
			cfg: checkprometheus.PrometheusConfig{
				URL: "https://x", Mode: checkprometheus.ModePromQL, CriticalValue: f64(1),
			},
			wantErr: "query",
		},
		{
			name: "unknown operator",
			cfg: checkprometheus.PrometheusConfig{
				URL: "https://x", Metric: "m", Operator: "=~", CriticalValue: f64(1),
			},
			wantErr: "operator",
		},
		{
			name:    "at least one threshold",
			cfg:     checkprometheus.PrometheusConfig{URL: "https://x", Metric: "m"},
			wantErr: "criticalValue",
		},
		{
			name: "warning-only is valid and can never page",
			cfg: checkprometheus.PrometheusConfig{
				URL: "https://x", Metric: "m", WarningValue: f64(5),
			},
		},
		{
			name: "zero warning is a real threshold, not unset",
			cfg: checkprometheus.PrometheusConfig{
				URL: "https://x", Metric: "m", Operator: checkprometheus.OpLessEqual,
				WarningValue: f64(0),
			},
		},
		{
			name: "greater ordering rejected",
			cfg: checkprometheus.PrometheusConfig{
				URL: "https://x", Metric: "m", Operator: checkprometheus.OpGreater,
				WarningValue: f64(100), CriticalValue: f64(10),
			},
			wantErr: "criticalValue",
		},
		{
			name: "greater ordering accepted",
			cfg: checkprometheus.PrometheusConfig{
				URL: "https://x", Metric: "m", Operator: checkprometheus.OpGreaterEqual,
				WarningValue: f64(10), CriticalValue: f64(100),
			},
		},
		{
			name: "less ordering rejected",
			cfg: checkprometheus.PrometheusConfig{
				URL: "https://x", Metric: "m", Operator: checkprometheus.OpLess,
				WarningValue: f64(10), CriticalValue: f64(100),
			},
			wantErr: "criticalValue",
		},
		{
			name: "less ordering accepted",
			cfg: checkprometheus.PrometheusConfig{
				URL: "https://x", Metric: "m", Operator: checkprometheus.OpLessEqual,
				WarningValue: f64(100), CriticalValue: f64(10),
			},
		},
		{
			name: "equality rejects a warning tier",
			cfg: checkprometheus.PrometheusConfig{
				URL: "https://x", Metric: "m", Operator: checkprometheus.OpEqual,
				WarningValue: f64(1), CriticalValue: f64(0),
			},
			wantErr: "warningValue",
		},
		{
			name: "equality with critical only",
			cfg: checkprometheus.PrometheusConfig{
				URL: "https://x", Metric: "m", Operator: checkprometheus.OpNotEqual,
				CriticalValue: f64(1),
			},
		},
		{
			name: "unknown match",
			cfg: checkprometheus.PrometheusConfig{
				URL: "https://x", Metric: "m", CriticalValue: f64(1), Match: "first",
			},
			wantErr: "match",
		},
		{
			name: "unknown onMissing",
			cfg: checkprometheus.PrometheusConfig{
				URL: "https://x", Metric: "m", CriticalValue: f64(1), OnMissing: "ignore",
			},
			wantErr: "onMissing",
		},
		{
			name: "timeout cap",
			cfg: checkprometheus.PrometheusConfig{
				URL: "https://x", Metric: "m", CriticalValue: f64(1), Timeout: 5 * time.Minute,
			},
			wantErr: "timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				r.NoError(err)

				return
			}

			r.Error(err)
			r.Contains(err.Error(), tt.wantErr)
		})
	}
}

// TestPrometheusConfig_ZeroThresholdIsSet is the pointer-presence trap called
// out by the spec: thresholds are float64 and 0 is legal, so `warningValue: 0`
// coming off a JSON config must decode to a SET threshold, not to "unset".
func TestPrometheusConfig_ZeroThresholdIsSet(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &checkprometheus.PrometheusConfig{}
	r.NoError(cfg.FromMap(map[string]any{
		"url": "https://x/metrics", "metric": "m", "operator": "<=", "warningValue": float64(0),
	}))

	r.NotNil(cfg.WarningValue, "warningValue: 0 must decode as SET, not as unset")
	r.InDelta(0.0, *cfg.WarningValue, 1e-9)
	r.Nil(cfg.CriticalValue)

	// And it survives validation as the only tier.
	r.NoError(cfg.Validate())

	// Round-tripping keeps the explicit zero.
	round := &checkprometheus.PrometheusConfig{}
	r.NoError(round.FromMap(cfg.GetConfig()))
	r.NotNil(round.WarningValue)
	r.InDelta(0.0, *round.WarningValue, 1e-9)
}

func TestPrometheusConfig_FromMap(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &checkprometheus.PrometheusConfig{}
	r.NoError(cfg.FromMap(map[string]any{
		"url":           "https://x/metrics",
		"mode":          "scrape",
		"metric":        "process_open_fds",
		"labels":        map[string]any{"instance": "app-1"},
		"headers":       map[string]any{"Authorization": "Bearer t"},
		"operator":      ">",
		"warningValue":  800,
		"criticalValue": float64(1000),
		"match":         "max",
		"onMissing":     "warning",
		"timeout":       "20s",
	}))

	r.Equal("process_open_fds", cfg.Metric)
	r.Equal(map[string]string{"instance": "app-1"}, cfg.Labels)
	r.Equal(map[string]string{"Authorization": "Bearer t"}, cfg.Headers)
	r.InDelta(800.0, *cfg.WarningValue, 1e-9)
	r.InDelta(1000.0, *cfg.CriticalValue, 1e-9)
	r.Equal("max", cfg.EffectiveMatch())
	r.Equal("warning", cfg.EffectiveOnMissing())
	r.Equal(20*time.Second, cfg.Timeout)
}

func TestPrometheusConfig_FromMap_TypeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      map[string]any
		wantErr string
	}{
		{"url not a string", map[string]any{"url": 12}, "url"},
		{"threshold not a number", map[string]any{"warningValue": "many"}, "warningValue"},
		{"labels not an object", map[string]any{"labels": "a=b"}, "labels"},
		{"label value not a string", map[string]any{"labels": map[string]any{"a": 1}}, "labels"},
		{"timeout not a duration", map[string]any{"timeout": "soon"}, "timeout"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			cfg := &checkprometheus.PrometheusConfig{}
			err := cfg.FromMap(tt.in)
			r.Error(err)
			r.Contains(err.Error(), tt.wantErr)
		})
	}
}

func TestPrometheusConfig_Defaults(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &checkprometheus.PrometheusConfig{}
	r.Equal(checkprometheus.ModeScrape, cfg.EffectiveMode())
	r.Equal(checkprometheus.OpGreater, cfg.EffectiveOperator())
	r.Equal(checkprometheus.MatchSingle, cfg.EffectiveMatch())
	r.Equal(checkprometheus.OnMissingDown, cfg.EffectiveOnMissing())
	r.Positive(cfg.EffectiveTimeout())
}

// TestPrometheusChecker_Validate_FillsNameAndSlug pins the default slug: a
// checker whose Validate leaves spec.Slug empty regresses the
// checkdnsbl/checksip audit gap.
func TestPrometheusChecker_Validate_FillsNameAndSlug(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	checker := &checkprometheus.PrometheusChecker{}
	spec := &checkerdef.CheckSpec{Config: map[string]any{
		"url": "https://app.example.com/metrics", "metric": "process_open_fds", "criticalValue": 10,
	}}

	r.NoError(checker.Validate(spec))
	r.NotEmpty(spec.Slug)
	r.Equal("prometheus-app-example-com", spec.Slug)
	r.Contains(spec.Name, "process_open_fds")
}

func TestPrometheusChecker_SampleConfigs(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	checker := &checkprometheus.PrometheusChecker{}
	samples := checker.GetSampleConfigs(&checkerdef.ListSampleOptions{})
	r.Len(samples, 2)

	modes := make(map[string]bool, len(samples))

	for _, sample := range samples {
		r.NotEmpty(sample.Slug, "sample %q must carry a slug", sample.Name)
		r.NotEmpty(sample.Name)

		spec := &checkerdef.CheckSpec{Name: sample.Name, Slug: sample.Slug, Config: sample.Config}
		r.NoError(checker.Validate(spec), "sample %q must validate", sample.Name)

		cfg := &checkprometheus.PrometheusConfig{}
		r.NoError(cfg.FromMap(sample.Config))
		modes[cfg.EffectiveMode()] = true
	}

	r.True(modes[checkprometheus.ModeScrape], "a scrape sample is required")
	r.True(modes[checkprometheus.ModePromQL], "a promql sample is required")
}
