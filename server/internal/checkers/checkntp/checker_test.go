package checkntp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/beevik/ntp"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkntp"
)

// errQuery is a synthetic transport error fed to the checker.
var errQuery = errors.New("connection refused")

func TestNTPConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		config     map[string]any
		wantErr    bool
		errSubstr  string
		wantPort   int
		wantVer    int
		checkExtra func(r *require.Assertions, spec *checkerdef.CheckSpec)
	}{
		{
			name:      "missing host",
			config:    map[string]any{},
			wantErr:   true,
			errSubstr: "host",
		},
		{
			name:     "defaults applied",
			config:   map[string]any{"host": "pool.ntp.org"},
			wantPort: 123,
			wantVer:  4,
			checkExtra: func(r *require.Assertions, spec *checkerdef.CheckSpec) {
				r.Equal("NTP: pool.ntp.org", spec.Name)
				r.Equal("ntp-pool-ntp-org", spec.Slug)
			},
		},
		{
			name:      "port out of range",
			config:    map[string]any{"host": "h", "port": 70000},
			wantErr:   true,
			errSubstr: "port",
		},
		{
			name:     "port too low",
			config:   map[string]any{"host": "h", "port": 0.0, "version": 4},
			wantPort: 123, // 0 -> default
			wantVer:  4,
			wantErr:  false,
		},
		{
			name:      "timeout too large",
			config:    map[string]any{"host": "h", "timeout": "90s"},
			wantErr:   true,
			errSubstr: "timeout",
		},
		{
			name:      "invalid version",
			config:    map[string]any{"host": "h", "version": 5},
			wantErr:   true,
			errSubstr: "version",
		},
		{
			name:     "version 3 ok",
			config:   map[string]any{"host": "h", "version": 3},
			wantPort: 123,
			wantVer:  3,
		},
		{
			name:      "warn greater than crit",
			config:    map[string]any{"host": "h", "offset_warn_ms": 500, "offset_crit_ms": 100},
			wantErr:   true,
			errSubstr: "offset_warn_ms",
		},
		{
			name:     "warn <= crit ok",
			config:   map[string]any{"host": "h", "offset_warn_ms": 100, "offset_crit_ms": 500},
			wantPort: 123,
			wantVer:  4,
		},
		{
			name:      "negative max stratum",
			config:    map[string]any{"host": "h", "max_stratum": -1},
			wantErr:   true,
			errSubstr: "max_stratum",
		},
		{
			name:      "max stratum above cap",
			config:    map[string]any{"host": "h", "max_stratum": 16},
			wantErr:   true,
			errSubstr: "max_stratum",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			checker := &checkntp.NTPChecker{}
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

			cfg := &checkntp.NTPConfig{}
			r.NoError(cfg.FromMap(tt.config))
			r.NoError(cfg.Validate())

			if tt.wantPort != 0 {
				r.Equal(tt.wantPort, cfg.Port)
			}

			if tt.wantVer != 0 {
				r.Equal(tt.wantVer, cfg.Version)
			}

			if tt.checkExtra != nil {
				tt.checkExtra(r, spec)
			}
		})
	}
}

func TestNTPConfig_RoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	orig := &checkntp.NTPConfig{
		Host:         "time.google.com",
		Port:         1230,
		Timeout:      7 * time.Second,
		Version:      3,
		OffsetWarnMs: 50,
		OffsetCritMs: 250,
		MaxStratum:   10,
	}

	round := &checkntp.NTPConfig{}
	r.NoError(round.FromMap(orig.GetConfig()))
	r.Equal(orig, round)
}

// healthyResponse returns a valid stratum-2 response with a small offset.
func healthyResponse() *ntp.Response {
	return &ntp.Response{
		Stratum:        2,
		Leap:           ntp.LeapNoWarning,
		ClockOffset:    12 * time.Millisecond,
		RTT:            8 * time.Millisecond,
		RootDelay:      5 * time.Millisecond,
		RootDispersion: 3 * time.Millisecond,
		RootDistance:   6 * time.Millisecond,
		Poll:           64 * time.Second,
		Precision:      time.Millisecond,
		ReferenceID:    0x4C4F434C,
		Time:           time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC),
		ReferenceTime:  time.Date(2026, 6, 29, 11, 59, 0, 0, time.UTC),
	}
}

func TestNTPChecker_Execute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cfg        *checkntp.NTPConfig
		resp       *ntp.Response
		queryErr   error
		ctxFunc    func() (context.Context, context.CancelFunc)
		wantStatus checkerdef.Status
	}{
		{
			name:       "healthy stratum 2 up",
			cfg:        &checkntp.NTPConfig{Host: "h"},
			resp:       healthyResponse(),
			wantStatus: checkerdef.StatusUp,
		},
		{
			name: "stratum 16 unsynchronized down",
			cfg:  &checkntp.NTPConfig{Host: "h"},
			resp: func() *ntp.Response {
				r := healthyResponse()
				r.Stratum = 16
				r.RootDistance = time.Second

				return r
			}(),
			wantStatus: checkerdef.StatusDown,
		},
		{
			name: "stratum 0 kiss of death down",
			cfg:  &checkntp.NTPConfig{Host: "h"},
			resp: func() *ntp.Response {
				r := healthyResponse()
				r.Stratum = 0
				r.KissCode = "RATE"

				return r
			}(),
			wantStatus: checkerdef.StatusDown,
		},
		{
			name: "leap not in sync down",
			cfg:  &checkntp.NTPConfig{Host: "h"},
			resp: func() *ntp.Response {
				r := healthyResponse()
				r.Leap = ntp.LeapNotInSync

				return r
			}(),
			wantStatus: checkerdef.StatusDown,
		},
		{
			name: "offset over warn within crit warning",
			cfg:  &checkntp.NTPConfig{Host: "h", OffsetWarnMs: 5, OffsetCritMs: 100},
			resp: func() *ntp.Response {
				r := healthyResponse()
				r.ClockOffset = 12 * time.Millisecond

				return r
			}(),
			wantStatus: checkerdef.StatusWarning,
		},
		{
			name: "negative offset over warn warning",
			cfg:  &checkntp.NTPConfig{Host: "h", OffsetWarnMs: 5, OffsetCritMs: 100},
			resp: func() *ntp.Response {
				r := healthyResponse()
				r.ClockOffset = -12 * time.Millisecond

				return r
			}(),
			wantStatus: checkerdef.StatusWarning,
		},
		{
			name: "offset over crit down",
			cfg:  &checkntp.NTPConfig{Host: "h", OffsetWarnMs: 5, OffsetCritMs: 10},
			resp: func() *ntp.Response {
				r := healthyResponse()
				r.ClockOffset = 50 * time.Millisecond

				return r
			}(),
			wantStatus: checkerdef.StatusDown,
		},
		{
			name: "stratum over max down",
			cfg:  &checkntp.NTPConfig{Host: "h", MaxStratum: 1},
			resp: func() *ntp.Response {
				r := healthyResponse()
				r.Stratum = 3

				return r
			}(),
			wantStatus: checkerdef.StatusDown,
		},
		{
			name:       "transport error down",
			cfg:        &checkntp.NTPConfig{Host: "h"},
			queryErr:   errQuery,
			wantStatus: checkerdef.StatusDown,
		},
		{
			name:       "nil response down not panic",
			cfg:        &checkntp.NTPConfig{Host: "h"},
			resp:       nil,
			queryErr:   nil,
			wantStatus: checkerdef.StatusDown,
		},
		{
			name:     "deadline exceeded timeout",
			cfg:      &checkntp.NTPConfig{Host: "h"},
			queryErr: errQuery,
			ctxFunc: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel() // ctx.Err() != nil

				return ctx, cancel
			},
			wantStatus: checkerdef.StatusTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			ctx := context.Background()
			if tt.ctxFunc != nil {
				var cancel context.CancelFunc
				ctx, cancel = tt.ctxFunc()
				defer cancel()
			}

			ctx = checkntp.WithQueryFunc(ctx, func(_ string, _ ntp.QueryOptions) (*ntp.Response, error) {
				return tt.resp, tt.queryErr
			})

			checker := &checkntp.NTPChecker{}

			result, err := checker.Execute(ctx, tt.cfg)
			r.NoError(err)
			r.NotNil(result)
			r.Equal(tt.wantStatus, result.Status, "output: %v", result.Output)
		})
	}
}

func TestNTPChecker_MetricsAndOutput(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx := checkntp.WithQueryFunc(context.Background(),
		func(_ string, _ ntp.QueryOptions) (*ntp.Response, error) {
			return healthyResponse(), nil
		})

	checker := &checkntp.NTPChecker{}

	result, err := checker.Execute(ctx, &checkntp.NTPConfig{Host: "pool.ntp.org", Port: 123})
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status)

	// Duration mirrors RTT.
	r.Equal(8*time.Millisecond, result.Duration)

	// Metrics: present + correctly typed.
	for _, key := range []string{
		"offset_ms", "rtt_ms", "root_delay_ms", "root_dispersion_ms",
		"root_distance_ms", "poll_s", "precision_ms",
	} {
		_, ok := result.Metrics[key].(float64)
		r.Truef(ok, "metric %q should be float64, got %T", key, result.Metrics[key])
	}

	stratum, ok := result.Metrics["stratum"].(int)
	r.True(ok)
	r.Equal(2, stratum)

	r.InDelta(12.0, result.Metrics["offset_ms"], 0.001)

	// Output diagnostics.
	r.Equal("pool.ntp.org", result.Output[checkerdef.OutputKeyHost])
	r.Equal(123, result.Output[checkerdef.OutputKeyPort])
	r.Equal(2, result.Output["stratum"])
	r.Equal("no_warning", result.Output["leap"])
	r.Equal("+12ms", result.Output["offset"])
	r.Contains(result.Output, "server_time")
	r.Contains(result.Output, "reference_id")
}

func TestNTPChecker_KissCodeInOutput(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	resp := healthyResponse()
	resp.Stratum = 0
	resp.KissCode = "DENY"

	ctx := checkntp.WithQueryFunc(context.Background(),
		func(_ string, _ ntp.QueryOptions) (*ntp.Response, error) {
			return resp, nil
		})

	checker := &checkntp.NTPChecker{}

	result, err := checker.Execute(ctx, &checkntp.NTPConfig{Host: "h"})
	r.NoError(err)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal("DENY", result.Output["kiss_code"])
	r.Contains(result.Output, checkerdef.OutputKeyError)
}

func TestNTPChecker_Type(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &checkntp.NTPChecker{}
	r.Equal(checkerdef.CheckTypeNTP, checker.Type())
}
