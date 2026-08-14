package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/stretchr/testify/require"
)

const (
	testPathDashboard = "/dashboard"
	testPathStatus    = "/status"
	testHost5173      = "localhost:5173"
	testHost5174      = "localhost:5174"
)

func TestParseRedirectRule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected RedirectRule
		ok       bool
	}{
		{
			name:  "simple redirect with same path",
			input: "/dashboard:localhost:5173",
			expected: RedirectRule{
				PathPrefix: testPathDashboard,
				TargetHost: testHost5173,
				TargetPath: testPathDashboard,
			},
			ok: true,
		},
		{
			name:  "redirect with different target path",
			input: "/dashboard:localhost:5173/app",
			expected: RedirectRule{
				PathPrefix: testPathDashboard,
				TargetHost: testHost5173,
				TargetPath: "/app",
			},
			ok: true,
		},
		{
			name:  "root redirect",
			input: "/:localhost:5173",
			expected: RedirectRule{
				PathPrefix: "/",
				TargetHost: testHost5173,
				TargetPath: "/",
			},
			ok: true,
		},
		{
			name:  "redirect with nested target path",
			input: "/api:localhost:8080/v1/api",
			expected: RedirectRule{
				PathPrefix: "/api",
				TargetHost: "localhost:8080",
				TargetPath: "/v1/api",
			},
			ok: true,
		},
	}

	runRedirectRuleTests(t, tests)
}

func TestParseRedirectRuleInvalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected RedirectRule
		ok       bool
	}{
		{
			name:     "missing leading slash",
			input:    "dashboard:localhost:5173",
			expected: RedirectRule{},
			ok:       false,
		},
		{
			name:     "missing target",
			input:    "/dashboard:",
			expected: RedirectRule{},
			ok:       false,
		},
		{
			name:     "no colon separator",
			input:    "/dashboard",
			expected: RedirectRule{},
			ok:       false,
		},
		{
			name:     "empty input",
			input:    "",
			expected: RedirectRule{},
			ok:       false,
		},
	}

	runRedirectRuleTests(t, tests)
}

func runRedirectRuleTests(
	t *testing.T,
	tests []struct {
		name     string
		input    string
		expected RedirectRule
		ok       bool
	},
) {
	t.Helper()

	for index := range tests {
		testCase := &tests[index]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result, isValid := parseRedirectRule(testCase.input)
			if isValid != testCase.ok {
				t.Errorf("parseRedirectRule(%q) ok = %v, want %v", testCase.input, isValid, testCase.ok)

				return
			}

			if isValid && result != testCase.expected {
				t.Errorf("parseRedirectRule(%q) = %+v, want %+v", testCase.input, result, testCase.expected)
			}
		})
	}
}

func TestParseRedirects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected []RedirectRule
	}{
		{
			name:     "empty string",
			input:    "",
			expected: nil,
		},
		{
			name:  "single rule",
			input: "/dashboard:localhost:5173",
			expected: []RedirectRule{
				{PathPrefix: testPathDashboard, TargetHost: testHost5173, TargetPath: testPathDashboard},
			},
		},
		{
			name:  "multiple rules",
			input: "/dashboard:localhost:5173/dashboard,/status:localhost:5174/status",
			expected: []RedirectRule{
				{PathPrefix: testPathDashboard, TargetHost: testHost5173, TargetPath: testPathDashboard},
				{PathPrefix: testPathStatus, TargetHost: testHost5174, TargetPath: testPathStatus},
			},
		},
		{
			name:  "rules sorted by path length",
			input: "/:localhost:5173,/dashboard/settings:localhost:5173,/dashboard:localhost:5173",
			expected: []RedirectRule{
				{PathPrefix: "/dashboard/settings", TargetHost: testHost5173, TargetPath: "/dashboard/settings"},
				{PathPrefix: testPathDashboard, TargetHost: testHost5173, TargetPath: testPathDashboard},
				{PathPrefix: "/", TargetHost: testHost5173, TargetPath: "/"},
			},
		},
		{
			name:  "whitespace handling",
			input: " /dashboard:localhost:5173 , /status:localhost:5174 ",
			expected: []RedirectRule{
				{PathPrefix: testPathDashboard, TargetHost: testHost5173, TargetPath: testPathDashboard},
				{PathPrefix: testPathStatus, TargetHost: testHost5174, TargetPath: testPathStatus},
			},
		},
		{
			name:  "invalid rules skipped",
			input: "/valid:localhost:5173,invalid,/also-valid:localhost:5174",
			expected: []RedirectRule{
				{PathPrefix: "/also-valid", TargetHost: testHost5174, TargetPath: "/also-valid"},
				{PathPrefix: "/valid", TargetHost: testHost5173, TargetPath: "/valid"},
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result := parseRedirects(testCase.input)
			if len(result) != len(testCase.expected) {
				t.Errorf("parseRedirects(%q) returned %d rules, want %d", testCase.input, len(result), len(testCase.expected))

				return
			}

			for i := range result {
				if result[i] != testCase.expected[i] {
					t.Errorf("parseRedirects(%q)[%d] = %+v, want %+v", testCase.input, i, result[i], testCase.expected[i])
				}
			}
		})
	}
}

func TestParseLogLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected slog.Level
	}{
		{
			name:     "debug lowercase",
			input:    "debug",
			expected: slog.LevelDebug,
		},
		{
			name:     "debug uppercase",
			input:    "DEBUG",
			expected: slog.LevelDebug,
		},
		{
			name:     "info lowercase",
			input:    "info",
			expected: slog.LevelInfo,
		},
		{
			name:     "info uppercase",
			input:    "INFO",
			expected: slog.LevelInfo,
		},
		{
			name:     "warn lowercase",
			input:    "warn",
			expected: slog.LevelWarn,
		},
		{
			name:     "warning lowercase",
			input:    "warning",
			expected: slog.LevelWarn,
		},
		{
			name:     "warn uppercase",
			input:    "WARN",
			expected: slog.LevelWarn,
		},
		{
			name:     "error lowercase",
			input:    "error",
			expected: slog.LevelError,
		},
		{
			name:     "error uppercase",
			input:    "ERROR",
			expected: slog.LevelError,
		},
		{
			name:     "empty string defaults to info",
			input:    "",
			expected: slog.LevelInfo,
		},
		{
			name:     "invalid value defaults to info",
			input:    "invalid",
			expected: slog.LevelInfo,
		},
		{
			name:     "whitespace trimmed",
			input:    "  debug  ",
			expected: slog.LevelDebug,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result := ParseLogLevel(testCase.input)
			if result != testCase.expected {
				t.Errorf("ParseLogLevel(%q) = %v, want %v", testCase.input, result, testCase.expected)
			}
		})
	}
}

// TestApplyFileStorageEnv confirms the manual reader bypasses koanf's env
// underscore→dot collapse: every SP_FILESTORAGE_S3_* var lands on the
// snake_case-tagged FileStorageConfig field. Uses t.Setenv, which is
// incompatible with t.Parallel.
func TestApplyFileStorageEnv(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_FILESTORAGE_S3_BUCKET", "solidping")
	t.Setenv("SP_FILESTORAGE_S3_REGION", "eu-west-3")
	t.Setenv("SP_FILESTORAGE_S3_PREFIX", "blobs")
	t.Setenv("SP_FILESTORAGE_S3_ENDPOINT", "https://minio.local:9000")
	t.Setenv("SP_FILESTORAGE_S3_USE_PATH_STYLE", "true")
	t.Setenv("SP_FILESTORAGE_S3_ACCESS_KEY", "minio")
	t.Setenv("SP_FILESTORAGE_S3_SECRET_KEY", "minio123")

	var cfg FileStorageConfig
	applyFileStorageEnv(&cfg)

	r.Equal("solidping", cfg.S3Bucket)
	r.Equal("eu-west-3", cfg.S3Region)
	r.Equal("blobs", cfg.S3Prefix)
	r.Equal("https://minio.local:9000", cfg.S3Endpoint)
	r.True(cfg.S3UsePathStyle)
	r.Equal("minio", cfg.S3AccessKey)
	r.Equal("minio123", cfg.S3SecretKey)
}

// TestApplyProfilerEnv confirms SP_PROFILER_BLOCK_RATE / _MUTEX_FRACTION land on
// the snake_case-tagged ProfilerConfig fields despite koanf's env
// underscore→dot collapse. Uses t.Setenv, which is incompatible with t.Parallel.
func TestApplyProfilerEnv(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_PROFILER_BLOCK_RATE", "5")
	t.Setenv("SP_PROFILER_MUTEX_FRACTION", "7")

	cfg := ProfilerConfig{}
	applyProfilerEnv(&cfg)

	r.Equal(5, cfg.BlockRate)
	r.Equal(7, cfg.MutexFraction)
}

// TestApplyRealtimeEnv confirms the multi-word SP_REALTIME_* knobs land on the
// snake_case-tagged RealtimeConfig fields despite koanf's env underscore→dot
// collapse. Uses t.Setenv, which is incompatible with t.Parallel.
// TestApplyAuthEnv confirms SP_AUTH_ACCESS_TOKEN_EXPIRY/SP_AUTH_REFRESH_TOKEN_EXPIRY
// land on the snake_case-tagged AuthConfig fields despite koanf's env
// underscore→dot collapse. Discovered via a real E2E run of
// e2e/session-continuity.spec.ts (its documented side-car recipe silently
// no-op'd on this env var, since koanf's plain env loader would have mapped
// it to auth.access.token.expiry and missed the "access_token_expiry" tag).
// Uses t.Setenv, which is incompatible with t.Parallel.
func TestApplyAuthEnv(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_AUTH_ACCESS_TOKEN_EXPIRY", "10s")
	t.Setenv("SP_AUTH_REFRESH_TOKEN_EXPIRY", "48h")

	cfg := AuthConfig{AccessTokenExpiry: time.Hour, RefreshTokenExpiry: 7 * 24 * time.Hour}
	applyAuthEnv(&cfg)

	r.Equal(10*time.Second, cfg.AccessTokenExpiry)
	r.Equal(48*time.Hour, cfg.RefreshTokenExpiry)
}

// TestApplyAuthEnv_InvalidKeepsExisting mirrors the other applyXEnv
// "invalid input" tests: an unparseable duration must not zero out or
// otherwise corrupt the existing value.
func TestApplyAuthEnv_InvalidKeepsExisting(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_AUTH_ACCESS_TOKEN_EXPIRY", "not-a-duration")

	cfg := AuthConfig{AccessTokenExpiry: time.Hour}
	applyAuthEnv(&cfg)

	r.Equal(time.Hour, cfg.AccessTokenExpiry)
}

func TestApplyRealtimeEnv(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_REALTIME_FLUSH_INTERVAL", "2s")
	t.Setenv("SP_REALTIME_PING_INTERVAL", "40s")
	t.Setenv("SP_REALTIME_MAX_CONNECTIONS", "42")
	t.Setenv("SP_REALTIME_MAX_SUBSCRIPTIONS_PER_CONNECTION", "77")

	cfg := RealtimeConfig{
		Enabled: true, FlushInterval: time.Second, PingInterval: 25 * time.Second, MaxConnections: 1000,
		MaxSubscriptionsPerConnection: 512,
	}
	applyRealtimeEnv(&cfg)

	r.Equal(2*time.Second, cfg.FlushInterval)
	r.Equal(40*time.Second, cfg.PingInterval)
	r.Equal(42, cfg.MaxConnections)
	r.Equal(77, cfg.MaxSubscriptionsPerConnection)
}

// TestRealtimeDefaults confirms the realtime feature defaults to enabled with
// the documented flush/ping windows, connection cap, and per-connection
// subscription cap.
func TestRealtimeDefaults(t *testing.T) { //nolint:paralleltest // Load reads process env
	r := require.New(t)

	cfg, err := Load()
	r.NoError(err)

	r.True(cfg.Realtime.Enabled)
	r.Equal(time.Second, cfg.Realtime.FlushInterval)
	r.Equal(25*time.Second, cfg.Realtime.PingInterval)
	r.Equal(1000, cfg.Realtime.MaxConnections)
	r.Equal(512, cfg.Realtime.MaxSubscriptionsPerConnection)
}

// TestApplyRuntimeEnv confirms the SP_RUNTIME_* memory-guardrail knobs land on
// the snake_case-tagged RuntimeConfig fields despite koanf's env underscore→dot
// collapse. Uses t.Setenv, which is incompatible with t.Parallel.
func TestApplyRuntimeEnv(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_RUNTIME_MEMORY_LIMIT", "400MiB")
	t.Setenv("SP_RUNTIME_AUTO_MEMORY_LIMIT", "false")
	t.Setenv("SP_RUNTIME_MEMORY_LIMIT_RATIO", "0.75")
	t.Setenv("SP_RUNTIME_GC_PERCENT", "60")

	cfg := RuntimeConfig{AutoMemoryLimit: true, MemoryLimitRatio: 0.9}
	applyRuntimeEnv(&cfg)

	r.Equal("400MiB", cfg.MemoryLimit)
	r.False(cfg.AutoMemoryLimit)
	r.InEpsilon(0.75, cfg.MemoryLimitRatio, 1e-9)
	r.Equal(60, cfg.GCPercent)
}

// TestApplySchedulingLaneEnv confirms the SP_SCHEDULING_LANE_* / _FAST_LANE_*
// knobs land on the snake_case-tagged SchedulingConfig fields despite koanf's
// env underscore→dot collapse (spec 2026-07-01-03). Uses t.Setenv, which is
// incompatible with t.Parallel.
func TestApplySchedulingLaneEnv(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_SCHEDULING_LANE_SLOW_THRESHOLD_MS", "3000")
	t.Setenv("SP_SCHEDULING_LANE_FAST_THRESHOLD_MS", "1500")
	t.Setenv("SP_SCHEDULING_FAST_LANE_RESERVED", "7")

	cfg := SchedulingConfig{
		LaneSlowThresholdMs: 2000,
		LaneFastThresholdMs: 1000,
		FastLaneReserved:    5,
	}
	applySchedulingEnv(&cfg)

	r.InEpsilon(3000.0, cfg.LaneSlowThresholdMs, 1e-9)
	r.InEpsilon(1500.0, cfg.LaneFastThresholdMs, 1e-9)
	r.Equal(7, cfg.FastLaneReserved)
}

// TestSchedulingCheckTimeoutYAMLBinding confirms scheduling.check_timeout_ms
// binds from YAML through koanf onto CheckTimeoutMs via its koanf tag (spec
// 2026-07-10-11), using the same tag-based Unmarshal the real Load() uses.
func TestSchedulingCheckTimeoutYAMLBinding(t *testing.T) {
	t.Parallel()

	yamlDoc := "server:\n  scheduling:\n    check_timeout_ms: 12500\n"
	path := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(yamlDoc), 0o600))

	k := koanf.New(".")
	require.NoError(t, k.Load(file.Provider(path), yaml.Parser()))

	var cfg Config
	require.NoError(t, k.Unmarshal("", &cfg))
	require.InEpsilon(t, 12500.0, cfg.Server.Scheduling.CheckTimeoutMs, 1e-9,
		"scheduling.check_timeout_ms must bind from YAML")
}

// TestApplySchedulingCheckTimeoutEnv confirms SP_SCHEDULING_CHECK_TIMEOUT_MS
// lands on the snake_case-tagged CheckTimeoutMs field despite koanf's env
// underscore→dot collapse (spec 2026-07-10-11). Uses t.Setenv, incompatible
// with t.Parallel.
func TestApplySchedulingCheckTimeoutEnv(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_SCHEDULING_CHECK_TIMEOUT_MS", "9000")

	cfg := SchedulingConfig{CheckTimeoutMs: 15000}
	applySchedulingEnv(&cfg)

	r.InEpsilon(9000.0, cfg.CheckTimeoutMs, 1e-9)
}

// TestApplySchedulingCheckTimeoutEnv_InvalidKeepsExisting confirms a
// non-numeric env value leaves the existing default untouched.
func TestApplySchedulingCheckTimeoutEnv_InvalidKeepsExisting(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_SCHEDULING_CHECK_TIMEOUT_MS", "not-a-number")

	cfg := SchedulingConfig{CheckTimeoutMs: 15000}
	applySchedulingEnv(&cfg)

	r.InEpsilon(15000.0, cfg.CheckTimeoutMs, 1e-9)
}

// TestLoad_SchedulingCheckTimeoutDefault confirms Load() defaults the global
// check timeout to 15000ms (spec 2026-07-10-11) with no config file or env
// override in play, and that SP_SCHEDULING_CHECK_TIMEOUT_MS overrides it.
func TestLoad_SchedulingCheckTimeoutDefault(t *testing.T) {
	r := require.New(t)

	cfg, err := Load()
	r.NoError(err)
	r.InEpsilon(15000.0, cfg.Server.Scheduling.CheckTimeoutMs, 1e-9,
		"default global check timeout is 15000ms")

	t.Setenv("SP_SCHEDULING_CHECK_TIMEOUT_MS", "20000")
	cfg, err = Load()
	r.NoError(err)
	r.InEpsilon(20000.0, cfg.Server.Scheduling.CheckTimeoutMs, 1e-9,
		"SP_SCHEDULING_CHECK_TIMEOUT_MS overrides the default")
}

// TestValidateLaneThresholds covers the fast<slow lane-band validation (spec
// 2026-07-01-03): inverted or degenerate bands are startup errors, disabled
// classification (slow=0) is accepted, negatives are rejected.
func TestValidateLaneThresholds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fast    float64
		slow    float64
		wantErr bool
	}{
		{"defaults are valid", 1000, 2000, false},
		{"classifier disabled ignores fast", 5000, 0, false},
		{"zero band edges with classifier off", 0, 0, false},
		{"fast may be 0 with classifier on", 0, 2000, false},
		{"inverted thresholds rejected", 3000, 2000, true},
		{"equal thresholds rejected (no dead-band)", 2000, 2000, true},
		{"negative fast rejected", -1, 2000, true},
		{"negative slow rejected", 1000, -5, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := SchedulingConfig{
				LaneFastThresholdMs: tt.fast,
				LaneSlowThresholdMs: tt.slow,
			}
			err := validateLaneThresholds(&cfg)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidLaneThresholds)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestValidateCustomDomainTLSConfig covers the two fail-fast rules guarding
// custom-domain serving: an unknown CNAME verification mode must be rejected
// loudly rather than silently downgraded to "shared" (which would nullify
// token mode's takeover protection), and in-server ACME must not start without
// the inputs it cannot invent.
func TestValidateCustomDomainTLSConfig(t *testing.T) {
	t.Parallel()

	const email = "ops@solidping.io"

	tests := []struct {
		name    string
		mode    string
		acme    ACMEConfig
		wantErr error
	}{
		{name: "defaults (acme off, empty mode)", mode: ""},
		{name: "shared mode", mode: "shared"},
		{name: "token mode", mode: "token"},
		{name: "mode is case-insensitive", mode: "TOKEN"},
		{
			name:    "unknown mode rejected",
			mode:    "dns-txt",
			wantErr: ErrInvalidCNAMEMode,
		},
		{
			name: "acme enabled with everything set",
			mode: "shared",
			acme: ACMEConfig{
				Enabled: true, Email: email,
				ListenHTTP: DefaultACMEListenHTTP, ListenHTTPS: DefaultACMEListenHTTPS,
			},
		},
		{
			name:    "acme enabled without an email rejected",
			mode:    "shared",
			acme:    ACMEConfig{Enabled: true, ListenHTTP: DefaultACMEListenHTTP, ListenHTTPS: DefaultACMEListenHTTPS},
			wantErr: ErrACMEEmailRequired,
		},
		{
			name: "acme enabled with a blank email rejected",
			mode: "shared",
			acme: ACMEConfig{
				Enabled: true, Email: "   ",
				ListenHTTP: DefaultACMEListenHTTP, ListenHTTPS: DefaultACMEListenHTTPS,
			},
			wantErr: ErrACMEEmailRequired,
		},
		{
			name:    "acme enabled without the http listener rejected",
			mode:    "shared",
			acme:    ACMEConfig{Enabled: true, Email: email, ListenHTTPS: DefaultACMEListenHTTPS},
			wantErr: ErrACMEListenRequired,
		},
		{
			name:    "acme enabled without the https listener rejected",
			mode:    "shared",
			acme:    ACMEConfig{Enabled: true, Email: email, ListenHTTP: DefaultACMEListenHTTP},
			wantErr: ErrACMEListenRequired,
		},
		{
			// Disabled means "no behavior change at all", so an otherwise
			// incomplete block must not block startup.
			name: "acme disabled ignores its own fields",
			mode: "shared",
			acme: ACMEConfig{Enabled: false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := ServerConfig{CustomDomainCNAMEMode: tt.mode}
			acme := tt.acme

			err := validateCustomDomainTLSConfig(&server, &acme)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
		})
	}
}

// TestValidateACMEProxyProtocol pins the fail-closed precondition of the PROXY
// protocol trust policy: the header is unauthenticated, so switching it on
// without naming the sources allowed to send one would let any peer dictate the
// client IP the rate limiter sees.
func TestValidateACMEProxyProtocol(t *testing.T) {
	t.Parallel()

	base := func(mutate func(*ACMEConfig)) ACMEConfig {
		cfg := ACMEConfig{
			Enabled: true, Email: "ops@solidping.io",
			ListenHTTP: DefaultACMEListenHTTP, ListenHTTPS: DefaultACMEListenHTTPS,
		}
		mutate(&cfg)

		return cfg
	}

	tests := []struct {
		name    string
		acme    ACMEConfig
		wantErr error
	}{
		{
			name: "proxy protocol off needs no CIDRs",
			acme: base(func(*ACMEConfig) {}),
		},
		{
			name: "proxy protocol on with a CIDR range",
			acme: base(func(c *ACMEConfig) {
				c.ProxyProtocol = true
				c.ProxyProtocolTrustedCIDRs = []string{"10.42.0.0/16"}
			}),
		},
		{
			name: "proxy protocol on with a bare IP",
			acme: base(func(c *ACMEConfig) {
				c.ProxyProtocol = true
				c.ProxyProtocolTrustedCIDRs = []string{"10.42.0.7"}
			}),
		},
		{
			name: "proxy protocol on with no CIDRs rejected",
			acme: base(func(c *ACMEConfig) {
				c.ProxyProtocol = true
			}),
			wantErr: ErrACMEProxyProtocolCIDRsRequired,
		},
		{
			name: "proxy protocol on with only blank CIDRs rejected",
			acme: base(func(c *ACMEConfig) {
				c.ProxyProtocol = true
				c.ProxyProtocolTrustedCIDRs = []string{"", "   "}
			}),
			wantErr: ErrACMEProxyProtocolCIDRsRequired,
		},
		{
			name: "an unparseable range is rejected rather than ignored",
			acme: base(func(c *ACMEConfig) {
				c.ProxyProtocol = true
				c.ProxyProtocolTrustedCIDRs = []string{"10.42.0.0/99"}
			}),
			wantErr: ErrACMEProxyProtocolCIDRInvalid,
		},
		{
			name: "a non-address entry is rejected rather than ignored",
			acme: base(func(c *ACMEConfig) {
				c.ProxyProtocol = true
				c.ProxyProtocolTrustedCIDRs = []string{"traefik"}
			}),
			wantErr: ErrACMEProxyProtocolCIDRInvalid,
		},
		{
			// The whole ACME block is inert when disabled, this key included.
			name: "acme disabled ignores the proxy protocol block",
			acme: ACMEConfig{ProxyProtocol: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := ServerConfig{CustomDomainCNAMEMode: "shared"}
			acme := tt.acme

			err := validateCustomDomainTLSConfig(&server, &acme)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
		})
	}
}

// TestValidateACMEFallbackUpstreams pins the startup contract of the chained
// fallback: a next hop that is not a dialable host:port, or one configured on
// an edge that never listens, must fail at startup rather than surface later as
// "every unknown-host connection is silently dropped".
func TestValidateACMEFallbackUpstreams(t *testing.T) {
	t.Parallel()

	base := func(mutate func(*ACMEConfig)) ACMEConfig {
		cfg := ACMEConfig{
			Enabled: true, Email: "ops@solidping.io",
			ListenHTTP: DefaultACMEListenHTTP, ListenHTTPS: DefaultACMEListenHTTPS,
		}
		mutate(&cfg)

		return cfg
	}

	tests := []struct {
		name    string
		acme    ACMEConfig
		wantErr error
	}{
		{
			name: "no upstream is the default and stays valid",
			acme: base(func(*ACMEConfig) {}),
		},
		{
			name: "both upstreams as host:port",
			acme: base(func(c *ACMEConfig) {
				c.FallbackUpstreamHTTPS = "10.42.0.9:443"
				c.FallbackUpstreamHTTP = "dev.internal:80"
			}),
		},
		{
			name: "an IPv6 upstream in bracket form",
			acme: base(func(c *ACMEConfig) {
				c.FallbackUpstreamHTTPS = "[2001:db8::1]:443"
			}),
		},
		{
			name: "a bare host with no port is rejected",
			acme: base(func(c *ACMEConfig) {
				c.FallbackUpstreamHTTPS = "dev.internal"
			}),
			wantErr: ErrACMEFallbackUpstreamInvalid,
		},
		{
			name: "a port-only address is rejected",
			acme: base(func(c *ACMEConfig) {
				c.FallbackUpstreamHTTP = ":80"
			}),
			wantErr: ErrACMEFallbackUpstreamInvalid,
		},
		{
			name: "a non-numeric port is rejected",
			acme: base(func(c *ACMEConfig) {
				c.FallbackUpstreamHTTPS = "dev.internal:https"
			}),
			wantErr: ErrACMEFallbackUpstreamInvalid,
		},
		{
			name: "an out-of-range port is rejected",
			acme: base(func(c *ACMEConfig) {
				c.FallbackUpstreamHTTPS = "dev.internal:70000"
			}),
			wantErr: ErrACMEFallbackUpstreamInvalid,
		},
		{
			// The two ACME listeners are the only ones that forward, so a next
			// hop without them is a no-op the operator must hear about.
			name:    "an upstream without acme.enabled is rejected",
			acme:    ACMEConfig{FallbackUpstreamHTTPS: "10.42.0.9:443"},
			wantErr: ErrACMEFallbackUpstreamRequiresACME,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := ServerConfig{CustomDomainCNAMEMode: "shared"}
			acme := tt.acme

			err := validateCustomDomainTLSConfig(&server, &acme)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
		})
	}
}

// TestApplyACMEFallbackUpstreamEnv covers the same koanf quirk for the chained
// fallback keys, including the default-true boolean that only an explicit
// falsey value may turn off. Uses t.Setenv, which is incompatible with
// t.Parallel.
func TestApplyACMEFallbackUpstreamEnv(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_ACME_FALLBACK_UPSTREAM_HTTPS", "10.42.0.9:443")
	t.Setenv("SP_ACME_FALLBACK_UPSTREAM_HTTP", "10.42.0.9:80")

	cfg := ACMEConfig{FallbackUpstreamProxyProtocol: true}
	applyACMEEnv(&cfg)

	r.Equal("10.42.0.9:443", cfg.FallbackUpstreamHTTPS)
	r.Equal("10.42.0.9:80", cfg.FallbackUpstreamHTTP)
	r.True(cfg.FallbackUpstreamProxyProtocol, "an unset env var must keep the default")

	t.Setenv("SP_ACME_FALLBACK_UPSTREAM_PROXY_PROTOCOL", "false")
	applyACMEEnv(&cfg)
	r.False(cfg.FallbackUpstreamProxyProtocol)
}

// TestACMEFallbackDefaults pins the default of the only new key whose zero
// value is not its default: a chained hop must carry the client address unless
// the operator explicitly opts out — and forwarding itself stays off.
func TestACMEFallbackDefaults(t *testing.T) { //nolint:paralleltest // Load reads process env
	r := require.New(t)

	cfg, err := Load()
	r.NoError(err)

	r.True(cfg.ACME.FallbackUpstreamProxyProtocol)
	r.Empty(cfg.ACME.FallbackUpstreamHTTPS, "forwarding must be off by default")
	r.Empty(cfg.ACME.FallbackUpstreamHTTP, "forwarding must be off by default")
}

// TestApplyACMEProxyProtocolEnv covers the koanf quirk: both new keys contain
// underscores, so the env provider can never bind them and applyACMEEnv has to
// read them by hand. Uses t.Setenv, which is incompatible with t.Parallel.
func TestApplyACMEProxyProtocolEnv(t *testing.T) {
	t.Setenv("SP_ACME_PROXY_PROTOCOL", "true")
	t.Setenv("SP_ACME_PROXY_PROTOCOL_TRUSTED_CIDRS", " 10.42.0.0/16 ,10.43.0.0/16, ")

	cfg := ACMEConfig{}
	applyACMEEnv(&cfg)

	r := require.New(t)
	r.True(cfg.ProxyProtocol)
	r.Equal([]string{"10.42.0.0/16", "10.43.0.0/16"}, cfg.ProxyProtocolTrustedCIDRs,
		"entries are trimmed and a trailing comma must not become an empty range")
}

// TestApplyDatabasePoolEnv confirms the SP_DB_*_CONNS / SP_DB_CONN_MAX_LIFETIME /
// SP_DB_CONN_MAX_IDLE_TIME pool knobs land on the snake_case-tagged
// DatabaseConfig fields. Uses t.Setenv, which is incompatible with t.Parallel.
func TestApplyDatabasePoolEnv(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_DB_MAX_OPEN_CONNS", "40")
	t.Setenv("SP_DB_MAX_IDLE_CONNS", "8")
	t.Setenv("SP_DB_CONN_MAX_LIFETIME", "90m")
	t.Setenv("SP_DB_CONN_MAX_IDLE_TIME", "2m")

	cfg := DatabaseConfig{
		MaxOpenConns:    25,
		MaxIdleConns:    10,
		ConnMaxLifetime: time.Hour,
		ConnMaxIdleTime: 5 * time.Minute,
	}
	applyDatabasePoolEnv(&cfg)

	r.Equal(40, cfg.MaxOpenConns)
	r.Equal(8, cfg.MaxIdleConns)
	r.Equal(90*time.Minute, cfg.ConnMaxLifetime)
	r.Equal(2*time.Minute, cfg.ConnMaxIdleTime)
}

// TestApplyDatabasePoolEnv_InvalidConnMaxIdleTimeKeepsExisting confirms an
// unparsable SP_DB_CONN_MAX_IDLE_TIME leaves the existing value untouched,
// matching the fail-open pattern of the other duration knobs.
func TestApplyDatabasePoolEnv_InvalidConnMaxIdleTimeKeepsExisting(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_DB_CONN_MAX_IDLE_TIME", "not-a-duration")

	cfg := DatabaseConfig{ConnMaxIdleTime: 5 * time.Minute}
	applyDatabasePoolEnv(&cfg)

	r.Equal(5*time.Minute, cfg.ConnMaxIdleTime)
}

// TestApplyNodeRolePoolDefaults covers D1 (spec 2026-07-05-09): checks/jobs
// nodes get the smaller 8/2 pool, api/all/unknown roles keep 25/10, and a
// pool value that no longer matches the api/all struct-literal default (as
// if config.yml already set it explicitly) is left alone even for a
// checks/jobs role.
func TestApplyNodeRolePoolDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		nodeRole         string
		maxOpenConns     int
		maxIdleConns     int
		wantMaxOpenConns int
		wantMaxIdleConns int
	}{
		{
			name: "all role keeps api-sized pool", nodeRole: NodeRoleAll,
			maxOpenConns: 25, maxIdleConns: 10,
			wantMaxOpenConns: 25, wantMaxIdleConns: 10,
		},
		{
			name: "api role keeps api-sized pool", nodeRole: NodeRoleAPI,
			maxOpenConns: 25, maxIdleConns: 10,
			wantMaxOpenConns: 25, wantMaxIdleConns: 10,
		},
		{
			name: "checks role shrinks to worker-sized pool", nodeRole: NodeRoleChecks,
			maxOpenConns: 25, maxIdleConns: 10,
			wantMaxOpenConns: 8, wantMaxIdleConns: 2,
		},
		{
			name: "jobs role shrinks to worker-sized pool", nodeRole: NodeRoleJobs,
			maxOpenConns: 25, maxIdleConns: 10,
			wantMaxOpenConns: 8, wantMaxIdleConns: 2,
		},
		{
			name: "unknown role is left alone (validated elsewhere)", nodeRole: "bogus",
			maxOpenConns: 25, maxIdleConns: 10,
			wantMaxOpenConns: 25, wantMaxIdleConns: 10,
		},
		{
			name:         "checks role with pre-set MaxOpenConns override is left alone",
			nodeRole:     NodeRoleChecks,
			maxOpenConns: 40, maxIdleConns: 10, // 40 simulates a config.yml override
			wantMaxOpenConns: 40, wantMaxIdleConns: 2, // MaxIdleConns still at default, so it shrinks
		},
		{
			name:         "checks role with pre-set MaxIdleConns override is left alone",
			nodeRole:     NodeRoleChecks,
			maxOpenConns: 25, maxIdleConns: 6, // 6 simulates a config.yml override
			wantMaxOpenConns: 8, wantMaxIdleConns: 6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := DatabaseConfig{MaxOpenConns: tt.maxOpenConns, MaxIdleConns: tt.maxIdleConns}
			applyNodeRolePoolDefaults(&cfg, tt.nodeRole)

			r := require.New(t)
			r.Equal(tt.wantMaxOpenConns, cfg.MaxOpenConns)
			r.Equal(tt.wantMaxIdleConns, cfg.MaxIdleConns)
		})
	}
}

// TestLoad_NodeRolePoolPrecedence is an end-to-end test of D1 through the real
// Load() entry point: a checks node with no overrides gets the 8/2 pool, and
// explicit SP_DB_MAX_OPEN_CONNS/SP_DB_MAX_IDLE_CONNS still win over the role
// default. Uses t.Setenv, which is incompatible with t.Parallel.
func TestLoad_NodeRolePoolPrecedence(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_NODE_ROLE", NodeRoleChecks)
	t.Setenv("SP_NODE_REGION", "eu") // required by Validate() for the checks role
	t.Setenv("SP_REGION", "eu")

	cfg, err := Load()
	r.NoError(err)
	r.Equal(dbPoolMaxOpenConnsChecksDefault, cfg.Database.MaxOpenConns)
	r.Equal(dbPoolMaxIdleConnsChecksDefault, cfg.Database.MaxIdleConns)

	t.Setenv("SP_DB_MAX_OPEN_CONNS", "17")
	t.Setenv("SP_DB_MAX_IDLE_CONNS", "3")

	cfg, err = Load()
	r.NoError(err)
	r.Equal(17, cfg.Database.MaxOpenConns)
	r.Equal(3, cfg.Database.MaxIdleConns)
}

// TestLoad_NodeRoleAPIOverridePrecedence closes the "for any role" half of
// acceptance criterion 1 for the api/all side specifically: an api node
// keeps 25/10 by default, and SP_DB_MAX_OPEN_CONNS/SP_DB_MAX_IDLE_CONNS still
// win even though applyNodeRolePoolDefaults never touches this role's pool at
// all (unlike the checks/jobs case in TestLoad_NodeRolePoolPrecedence, where
// the role default must first be overwritten and then overridden again).
// Uses t.Setenv, which is incompatible with t.Parallel.
func TestLoad_NodeRoleAPIOverridePrecedence(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_NODE_ROLE", NodeRoleAPI)

	cfg, err := Load()
	r.NoError(err)
	r.Equal(dbPoolMaxOpenConnsDefault, cfg.Database.MaxOpenConns)
	r.Equal(dbPoolMaxIdleConnsDefault, cfg.Database.MaxIdleConns)

	t.Setenv("SP_DB_MAX_OPEN_CONNS", "60")
	t.Setenv("SP_DB_MAX_IDLE_CONNS", "20")

	cfg, err = Load()
	r.NoError(err)
	r.Equal(60, cfg.Database.MaxOpenConns)
	r.Equal(20, cfg.Database.MaxIdleConns)
}

// TestLoad_NodeRoleAllKeepsAPIPool is the api/all-side companion to
// TestLoad_NodeRolePoolPrecedence: confirms Load() with no SP_NODE_ROLE set
// (defaults to "all") keeps the larger pool. Unlike its sibling, this test
// sets no env vars, so it can run in parallel.
func TestLoad_NodeRoleAllKeepsAPIPool(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg, err := Load()
	r.NoError(err)
	r.Equal(dbPoolMaxOpenConnsDefault, cfg.Database.MaxOpenConns)
	r.Equal(dbPoolMaxIdleConnsDefault, cfg.Database.MaxIdleConns)
}

// TestApplyJobsEnv confirms SP_JOBS_* durations land on the snake_case-tagged
// JobsConfig fields despite koanf's env underscore→dot collapse. Uses t.Setenv,
// which is incompatible with t.Parallel.
func TestApplyJobsEnv(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_JOBS_STUCK_TIMEOUT", "42m")
	t.Setenv("SP_JOBS_REAPER_INTERVAL", "30s")

	cfg := JobsConfig{StuckTimeout: 15 * time.Minute, ReaperInterval: time.Minute}
	applyJobsEnv(&cfg)

	r.Equal(42*time.Minute, cfg.StuckTimeout)
	r.Equal(30*time.Second, cfg.ReaperInterval)
}

// TestApplyJobsEnv_InvalidKeepsExisting confirms an unparsable duration leaves
// the existing (default) value untouched rather than zeroing it.
func TestApplyJobsEnv_InvalidKeepsExisting(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_JOBS_STUCK_TIMEOUT", "not-a-duration")

	cfg := JobsConfig{StuckTimeout: 15 * time.Minute, ReaperInterval: time.Minute}
	applyJobsEnv(&cfg)

	r.Equal(15*time.Minute, cfg.StuckTimeout)
	r.Equal(time.Minute, cfg.ReaperInterval)
}

// TestApplyFileStorageEnv_PathStyleVariants checks the boolean parsing accepts
// "1" and rejects other truthy-looking values.
func TestApplyFileStorageEnv_PathStyleVariants(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_FILESTORAGE_S3_USE_PATH_STYLE", "1")
	var c1 FileStorageConfig
	applyFileStorageEnv(&c1)
	r.True(c1.S3UsePathStyle)

	t.Setenv("SP_FILESTORAGE_S3_USE_PATH_STYLE", "yes")
	var c2 FileStorageConfig
	applyFileStorageEnv(&c2)
	r.False(c2.S3UsePathStyle)
}

// defaultPasswordConfig returns the same PasswordConfig the Load() defaults
// literal installs — the historical argon2id profile.
func defaultPasswordConfig() PasswordConfig {
	return PasswordConfig{
		Algorithm: PasswordAlgorithmArgon2id,
		Argon2: Argon2Params{
			Memory:     64 * 1024,
			Time:       3,
			Threads:    4,
			KeyLength:  32,
			SaltLength: 16,
		},
		Bcrypt:        BcryptParams{Cost: 12},
		RehashOnLogin: true,
	}
}

// validBaseConfig returns a minimal Config that passes Validate, so password
// validation can be exercised in isolation.
func validBaseConfig() *Config {
	return &Config{
		Database: DatabaseConfig{Type: DatabaseTypeSQLite, Dir: "."},
		// Node.Name is pinned so Validate()'s worker-identity check never
		// depends on the machine's real hostname.
		Node: NodeConfig{Role: NodeRoleAll, Name: "test-node"},
		Aggregation: AggregationConfig{
			RetentionRaw:  24,
			RetentionHour: 7,
			RetentionDay:  2,
		},
		Deployment: DeploymentConfig{Mode: DeploymentModeSelfHosted},
		Auth:       AuthConfig{Password: defaultPasswordConfig()},
	}
}

// TestPasswordConfigDefaults asserts the Load() defaults reproduce today's exact
// argon2id profile, so upgrading the binary is a no-op until reconfigured.
func TestPasswordConfigDefaults(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	defaults := defaultPasswordConfig()
	r.Equal(PasswordAlgorithmArgon2id, defaults.Algorithm)
	r.Equal(uint32(64*1024), defaults.Argon2.Memory)
	r.Equal(uint32(3), defaults.Argon2.Time)
	r.Equal(uint8(4), defaults.Argon2.Threads)
	r.Equal(uint32(32), defaults.Argon2.KeyLength)
	r.Equal(uint32(16), defaults.Argon2.SaltLength)
	r.Equal(12, defaults.Bcrypt.Cost)
	r.True(defaults.RehashOnLogin, "rehash-on-login defaults to true (preserves legacy migration behavior)")
}

// TestApplyPasswordHashingEnv confirms SP_AUTH_PASSWORD_* lands on the
// snake_case-tagged fields despite koanf's env underscore→dot collapse. Uses
// t.Setenv, which is incompatible with t.Parallel.
func TestApplyPasswordHashingEnv(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_AUTH_PASSWORD_ALGORITHM", "bcrypt")
	t.Setenv("SP_AUTH_PASSWORD_ARGON2_MEMORY", "19456")
	t.Setenv("SP_AUTH_PASSWORD_ARGON2_TIME", "2")
	t.Setenv("SP_AUTH_PASSWORD_ARGON2_THREADS", "1")
	t.Setenv("SP_AUTH_PASSWORD_ARGON2_KEY_LENGTH", "24")
	t.Setenv("SP_AUTH_PASSWORD_ARGON2_SALT_LENGTH", "12")
	t.Setenv("SP_AUTH_PASSWORD_BCRYPT_COST", "11")
	t.Setenv("SP_AUTH_PASSWORD_REHASH_ON_LOGIN", "false")

	cfg := defaultPasswordConfig() // starts with RehashOnLogin: true
	applyPasswordHashingEnv(&cfg)

	r.Equal("bcrypt", cfg.Algorithm)
	r.Equal(uint32(19456), cfg.Argon2.Memory)
	r.Equal(uint32(2), cfg.Argon2.Time)
	r.Equal(uint8(1), cfg.Argon2.Threads)
	r.Equal(uint32(24), cfg.Argon2.KeyLength)
	r.Equal(uint32(12), cfg.Argon2.SaltLength)
	r.Equal(11, cfg.Bcrypt.Cost)
	r.False(cfg.RehashOnLogin, "SP_AUTH_PASSWORD_REHASH_ON_LOGIN=false must override the default")
}

// TestApplyPasswordHashingEnvIsIdempotent pins the safety property behind the
// deliberate dual read of SP_AUTH_PASSWORD_* (config.Load's early bootstrap path
// AND systemconfig's authoritative overlay both read it): applying the env on top
// of an already-env-applied config must be a no-op. This is what makes the
// overlap harmless — env can only ever set the same value, never drift, so the
// two read paths can never conflict. Uses t.Setenv, so it is not parallel.
func TestApplyPasswordHashingEnvIsIdempotent(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_AUTH_PASSWORD_ALGORITHM", "bcrypt")
	t.Setenv("SP_AUTH_PASSWORD_ARGON2_MEMORY", "19456")
	t.Setenv("SP_AUTH_PASSWORD_ARGON2_TIME", "2")
	t.Setenv("SP_AUTH_PASSWORD_ARGON2_THREADS", "1")
	t.Setenv("SP_AUTH_PASSWORD_BCRYPT_COST", "14")
	t.Setenv("SP_AUTH_PASSWORD_REHASH_ON_LOGIN", "false")

	cfg := defaultPasswordConfig()
	applyPasswordHashingEnv(&cfg)
	once := cfg

	// Re-apply the same env (mimicking the second read path) — must not change a
	// single field.
	applyPasswordHashingEnv(&cfg)
	r.Equal(once, cfg, "re-reading SP_AUTH_PASSWORD_* must be idempotent")
}

// TestValidatePasswordConfig covers the fail-fast validation: defaults pass,
// bcrypt with honored cost passes, and each misconfiguration is rejected.
func TestValidatePasswordConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*PasswordConfig)
		wantErr error
	}{
		{
			name:   "argon2id defaults ok",
			mutate: func(_ *PasswordConfig) {},
		},
		{
			name: "bcrypt cost honored ok",
			mutate: func(p *PasswordConfig) {
				p.Algorithm = PasswordAlgorithmBcrypt
				p.Bcrypt.Cost = 12
			},
		},
		{
			name: "unknown algorithm rejected",
			mutate: func(p *PasswordConfig) {
				p.Algorithm = "scrypt"
			},
			wantErr: ErrInvalidPasswordAlgorithm,
		},
		{
			name: "bcrypt cost too low rejected",
			mutate: func(p *PasswordConfig) {
				p.Algorithm = PasswordAlgorithmBcrypt
				p.Bcrypt.Cost = 9
			},
			wantErr: ErrInvalidBcryptCost,
		},
		{
			name: "bcrypt cost too high rejected",
			mutate: func(p *PasswordConfig) {
				p.Algorithm = PasswordAlgorithmBcrypt
				p.Bcrypt.Cost = 32
			},
			wantErr: ErrInvalidBcryptCost,
		},
		{
			name: "argon2 memory below floor rejected",
			mutate: func(p *PasswordConfig) {
				p.Argon2.Memory = 4096
			},
			wantErr: ErrInvalidArgon2Params,
		},
		{
			name: "argon2 key length below floor rejected",
			mutate: func(p *PasswordConfig) {
				p.Argon2.KeyLength = 8
			},
			wantErr: ErrInvalidArgon2Params,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			cfg := validBaseConfig()
			tc.mutate(&cfg.Auth.Password)

			err := cfg.Validate()
			if tc.wantErr != nil {
				r.ErrorIs(err, tc.wantErr)
			} else {
				r.NoError(err)
			}
		})
	}
}

// TestValidatePasswordParameter covers the single-key write-time validator used
// by the system-parameter handler. Values arrive as encoding/json types
// (float64 for numbers, string for the algorithm, bool for rehash). The bounds
// must match the fail-fast checks in Validate so a value saved through the UI can
// never abort the next startup.
func TestValidatePasswordParameter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     string
		value   any
		wantErr bool
	}{
		// Algorithm.
		{name: "algorithm argon2id ok", key: ParamKeyPasswordAlgorithm, value: "argon2id"},
		{name: "algorithm bcrypt ok", key: ParamKeyPasswordAlgorithm, value: "bcrypt"},
		{name: "algorithm sha1 rejected", key: ParamKeyPasswordAlgorithm, value: "sha1", wantErr: true},
		{name: "algorithm wrong type rejected", key: ParamKeyPasswordAlgorithm, value: float64(1), wantErr: true},
		// Argon2 memory (floor 8192).
		{name: "memory at floor ok", key: ParamKeyPasswordArgon2Memory, value: float64(8192)},
		{name: "memory 19456 ok", key: ParamKeyPasswordArgon2Memory, value: float64(19456)},
		{name: "memory 1024 rejected", key: ParamKeyPasswordArgon2Memory, value: float64(1024), wantErr: true},
		{name: "memory non-number rejected", key: ParamKeyPasswordArgon2Memory, value: "lots", wantErr: true},
		// Argon2 time (floor 1).
		{name: "time 1 ok", key: ParamKeyPasswordArgon2Time, value: float64(1)},
		{name: "time 0 rejected", key: ParamKeyPasswordArgon2Time, value: float64(0), wantErr: true},
		// Argon2 threads (1..255).
		{name: "threads 1 ok", key: ParamKeyPasswordArgon2Threads, value: float64(1)},
		{name: "threads 255 ok", key: ParamKeyPasswordArgon2Threads, value: float64(255)},
		{name: "threads 0 rejected", key: ParamKeyPasswordArgon2Threads, value: float64(0), wantErr: true},
		{name: "threads 256 rejected", key: ParamKeyPasswordArgon2Threads, value: float64(256), wantErr: true},
		// Argon2 key/salt length floors.
		{name: "key_length 16 ok", key: ParamKeyPasswordArgon2KeyLen, value: float64(16)},
		{name: "key_length 8 rejected", key: ParamKeyPasswordArgon2KeyLen, value: float64(8), wantErr: true},
		{name: "salt_length 8 ok", key: ParamKeyPasswordArgon2SaltLen, value: float64(8)},
		{name: "salt_length 4 rejected", key: ParamKeyPasswordArgon2SaltLen, value: float64(4), wantErr: true},
		// Bcrypt cost (10..31).
		{name: "cost 10 ok", key: ParamKeyPasswordBcryptCost, value: float64(10)},
		{name: "cost 31 ok", key: ParamKeyPasswordBcryptCost, value: float64(31)},
		{name: "cost 9 rejected", key: ParamKeyPasswordBcryptCost, value: float64(9), wantErr: true},
		{name: "cost 99 rejected", key: ParamKeyPasswordBcryptCost, value: float64(99), wantErr: true},
		// Rehash bool.
		{name: "rehash true ok", key: ParamKeyPasswordRehashOnLogin, value: true},
		{name: "rehash false ok", key: ParamKeyPasswordRehashOnLogin, value: false},
		{name: "rehash string rejected", key: ParamKeyPasswordRehashOnLogin, value: "soon", wantErr: true},
		// Unknown key.
		{name: "unknown key rejected", key: "auth.password.unknown", value: float64(1), wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			err := ValidatePasswordParameter(tc.key, tc.value)
			if tc.wantErr {
				r.ErrorIs(err, ErrInvalidPasswordParameter)
			} else {
				r.NoError(err)
			}
		})
	}
}

// TestIsPasswordParameterKey pins the set of keys the write handler routes
// through ValidatePasswordParameter.
func TestIsPasswordParameterKey(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, key := range []string{
		ParamKeyPasswordAlgorithm, ParamKeyPasswordArgon2Memory, ParamKeyPasswordArgon2Time,
		ParamKeyPasswordArgon2Threads, ParamKeyPasswordArgon2KeyLen, ParamKeyPasswordArgon2SaltLen,
		ParamKeyPasswordBcryptCost, ParamKeyPasswordRehashOnLogin,
	} {
		r.Truef(IsPasswordParameterKey(key), "%s should be a password parameter key", key)
	}

	r.False(IsPasswordParameterKey("server.base_url"))
	r.False(IsPasswordParameterKey("auth.jwt_secret"))
}

// TestApplyAgentEnv pins the manual SP_AGENT_* reader, including the
// SP_AGENT_PRINT_KEYS opt-in whose koanf path (agent.print_keys) is snake_case
// and therefore unreachable by koanf's env loader. Uses t.Setenv, which is
// incompatible with t.Parallel.
func TestApplyAgentEnv(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_AGENT_SERVER_URL", "https://master.example.com")
	t.Setenv("SP_AGENT_KEYS_FILE", "/data/agent-keys.json")
	t.Setenv("SP_AGENT_PRINT_KEYS", "true")

	cfg := AgentConfig{}
	applyAgentEnv(&cfg)

	r.Equal("https://master.example.com", cfg.ServerURL)
	r.Equal("/data/agent-keys.json", cfg.KeysFile)
	r.True(cfg.PrintKeys)
}

// TestApplyAgentEnvPrintKeysDefaultsOff asserts the safe default: absent or
// unparseable SP_AGENT_PRINT_KEYS never turns key printing on.
func TestApplyAgentEnvPrintKeysDefaultsOff(t *testing.T) {
	r := require.New(t)

	cfg := AgentConfig{}
	applyAgentEnv(&cfg)
	r.False(cfg.PrintKeys)

	t.Setenv("SP_AGENT_PRINT_KEYS", "yes-please")
	applyAgentEnv(&cfg)
	r.False(cfg.PrintKeys)

	t.Setenv("SP_AGENT_PRINT_KEYS", "false")
	applyAgentEnv(&cfg)
	r.False(cfg.PrintKeys)
}
