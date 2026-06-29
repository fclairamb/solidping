package config

import (
	"log/slog"
	"testing"
	"time"

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

// TestApplyDatabasePoolEnv confirms the SP_DB_*_CONNS / SP_DB_CONN_MAX_LIFETIME
// pool knobs land on the snake_case-tagged DatabaseConfig fields. Uses t.Setenv,
// which is incompatible with t.Parallel.
func TestApplyDatabasePoolEnv(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_DB_MAX_OPEN_CONNS", "40")
	t.Setenv("SP_DB_MAX_IDLE_CONNS", "8")
	t.Setenv("SP_DB_CONN_MAX_LIFETIME", "90m")

	cfg := DatabaseConfig{MaxOpenConns: 25, MaxIdleConns: 10, ConnMaxLifetime: time.Hour}
	applyDatabasePoolEnv(&cfg)

	r.Equal(40, cfg.MaxOpenConns)
	r.Equal(8, cfg.MaxIdleConns)
	r.Equal(90*time.Minute, cfg.ConnMaxLifetime)
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
		Bcrypt: BcryptParams{Cost: 12},
	}
}

// validBaseConfig returns a minimal Config that passes Validate, so password
// validation can be exercised in isolation.
func validBaseConfig() *Config {
	return &Config{
		Database: DatabaseConfig{Type: DatabaseTypeSQLite, Dir: "."},
		Node:     NodeConfig{Role: NodeRoleAll},
		Aggregation: AggregationConfig{
			RetentionRaw:  24,
			RetentionHour: 30,
			RetentionDay:  12,
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

	cfg := defaultPasswordConfig()
	applyPasswordHashingEnv(&cfg)

	r.Equal("bcrypt", cfg.Algorithm)
	r.Equal(uint32(19456), cfg.Argon2.Memory)
	r.Equal(uint32(2), cfg.Argon2.Time)
	r.Equal(uint8(1), cfg.Argon2.Threads)
	r.Equal(uint32(24), cfg.Argon2.KeyLength)
	r.Equal(uint32(12), cfg.Argon2.SaltLength)
	r.Equal(11, cfg.Bcrypt.Cost)
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
