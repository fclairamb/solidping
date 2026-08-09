package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// roleExpectation is the full subsystem answer for one SP_NODE_ROLE value.
type roleExpectation struct {
	api    bool
	jobs   bool
	checks bool
	agent  bool
}

// TestShouldRun_LegacySingleValuesUnchanged is the backward-compatibility guard
// for spec 2026-08-09-01: SP_NODE_ROLE is set in production deployment
// manifests, so every historic single value must select exactly the same
// subsystem set as before the role became a set. The expectations below are
// transcribed from the pre-change implementation
// (`Role == NodeRoleAll || Role == NodeRole<X>`, and `Role == NodeRoleAgent`
// for agent mode), NOT from the new one.
func TestShouldRun_LegacySingleValuesUnchanged(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		role string
		want roleExpectation
	}{
		{
			name: "all runs everything but is not agent mode",
			role: NodeRoleAll,
			want: roleExpectation{api: true, jobs: true, checks: true, agent: false},
		},
		{
			name: "api serves HTTP only",
			role: NodeRoleAPI,
			want: roleExpectation{api: true},
		},
		{
			name: "jobs runs the job processor only",
			role: NodeRoleJobs,
			want: roleExpectation{jobs: true},
		},
		{
			name: "checks runs the check executor only",
			role: NodeRoleChecks,
			want: roleExpectation{checks: true},
		},
		{
			name: "agent runs the deported worker only",
			role: NodeRoleAgent,
			want: roleExpectation{agent: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assertRoleExpectation(t, tt.role, tt.want)
		})
	}
}

// TestShouldRun_MultiValueRoles covers the combinations the spec exists for.
// "api,jobs" is the one the EU1/default region needs: the main pod keeps
// serving the dashboard/API and processing jobs while a separate checks-only
// (hostNetwork) node executes the checks.
func TestShouldRun_MultiValueRoles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		role string
		want roleExpectation
	}{
		{
			name: "api,jobs starts the API and the job processor but NOT the check worker",
			role: "api,jobs",
			want: roleExpectation{api: true, jobs: true, checks: false, agent: false},
		},
		{
			name: "jobs,api is order independent",
			role: "jobs,api",
			want: roleExpectation{api: true, jobs: true},
		},
		{
			name: "checks,jobs runs both worker loops without the API",
			role: "checks,jobs",
			want: roleExpectation{jobs: true, checks: true},
		},
		{
			name: "api,checks runs the API and the check worker without jobs",
			role: "api,checks",
			want: roleExpectation{api: true, checks: true},
		},
		{
			name: "api,jobs,checks is the explicit spelling of all",
			role: "api,jobs,checks",
			want: roleExpectation{api: true, jobs: true, checks: true},
		},
		{
			name: "surrounding whitespace is tolerated",
			role: " api , jobs ",
			want: roleExpectation{api: true, jobs: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assertRoleExpectation(t, tt.role, tt.want)
		})
	}
}

// assertRoleExpectation checks the four subsystem gates for one role value.
func assertRoleExpectation(t *testing.T, role string, want roleExpectation) {
	t.Helper()

	r := require.New(t)
	cfg := &Config{Node: NodeConfig{Role: role}}

	r.Equal(want.api, cfg.ShouldRunAPI(), "ShouldRunAPI for role %q", role)
	r.Equal(want.jobs, cfg.ShouldRunJobs(), "ShouldRunJobs for role %q", role)
	r.Equal(want.checks, cfg.ShouldRunChecks(), "ShouldRunChecks for role %q", role)
	r.Equal(want.agent, cfg.IsAgentMode(), "IsAgentMode for role %q", role)
}

// TestParseNodeRolesRejects is the positive-control companion of the tables
// above: a typo must abort startup, never quietly disable a subsystem.
func TestParseNodeRolesRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		role        string
		wantErr     error
		wantMessage string
	}{
		{
			name: "unknown role", role: "chekcs",
			wantErr: ErrInvalidNodeRole, wantMessage: "chekcs",
		},
		{
			name: "unknown role inside a list", role: "api,jbos",
			wantErr: ErrInvalidNodeRole, wantMessage: "jbos",
		},
		{
			name: "empty value", role: "",
			wantErr: ErrInvalidNodeRole,
		},
		{
			name: "blank value", role: "   ",
			wantErr: ErrInvalidNodeRole,
		},
		{
			name: "empty trailing segment", role: "api,",
			wantErr: ErrInvalidNodeRole, wantMessage: "empty entry",
		},
		{
			name: "empty leading segment", role: ",api",
			wantErr: ErrInvalidNodeRole, wantMessage: "empty entry",
		},
		{
			name: "empty inner segment", role: "api,,jobs",
			wantErr: ErrInvalidNodeRole, wantMessage: "empty entry",
		},
		{
			name: "duplicate segment", role: "api,api",
			wantErr: ErrInvalidNodeRole, wantMessage: "listed twice",
		},
		{
			name: "agent combined with api", role: "agent,api",
			wantErr: ErrExclusiveNodeRole, wantMessage: "agent",
		},
		{
			name: "agent combined with checks", role: "checks,agent",
			wantErr: ErrExclusiveNodeRole, wantMessage: "agent",
		},
		{
			name: "all combined with checks", role: "all,checks",
			wantErr: ErrExclusiveNodeRole, wantMessage: "all",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			roles, err := ParseNodeRoles(tt.role)
			r.Error(err)
			r.Nil(roles)
			r.ErrorIs(err, tt.wantErr)

			if tt.wantMessage != "" {
				r.Contains(err.Error(), tt.wantMessage)
			}

			// A rejected value must never look like a live subsystem selection.
			assertRoleExpectation(t, tt.role, roleExpectation{})
		})
	}
}

// TestNodeRoleSetString pins the canonical rendering used in messages.
func TestNodeRoleSetString(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	roles, err := ParseNodeRoles("jobs,api")
	r.NoError(err)
	r.Equal("api,jobs", roles.String())
	r.True(roles.Has(NodeRoleAPI))
	r.False(roles.Has(NodeRoleAll))
	r.False(roles.Runs(NodeRoleChecks))
}

// TestValidateNodeConfigRoles walks the startup validation for every role
// shape: the historic rules (region required for `checks` but never for `all`,
// server URL required for `agent`) must be untouched, and the new invalid
// combinations must fail fast with an actionable message.
func TestValidateNodeConfigRoles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		role      string
		region    string
		serverURL string
		wantErr   error
	}{
		{name: "all needs no region", role: NodeRoleAll},
		{name: "api needs no region", role: NodeRoleAPI},
		{name: "jobs needs no region", role: NodeRoleJobs},
		{name: "api,jobs needs no region", role: "api,jobs"},
		{name: "checks with a region is valid", role: NodeRoleChecks, region: "eu"},
		{
			name: "checks without a region is rejected", role: NodeRoleChecks,
			wantErr: ErrRegionRequiredForChecks,
		},
		{
			name: "api,checks without a region is rejected too", role: "api,checks",
			wantErr: ErrRegionRequiredForChecks,
		},
		{name: "api,checks with a region is valid", role: "api,checks", region: "eu"},
		{
			name: "agent needs a server URL", role: NodeRoleAgent,
			wantErr: ErrAgentServerURLRequired,
		},
		{name: "agent with a server URL is valid", role: NodeRoleAgent, serverURL: "https://example.com"},
		{
			name: "unknown role is rejected", role: "bogus",
			wantErr: ErrInvalidNodeRole,
		},
		{
			name: "agent cannot be combined", role: "agent,api", serverURL: "https://example.com",
			wantErr: ErrExclusiveNodeRole,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			cfg := &Config{
				// An explicit node name keeps the worker-identity leg of the
				// validation independent of the machine's hostname.
				Node:  NodeConfig{Role: tt.role, Region: tt.region, Name: "test-node"},
				Agent: AgentConfig{ServerURL: tt.serverURL},
			}

			err := cfg.validateNodeConfig()
			if tt.wantErr == nil {
				r.NoError(err)

				return
			}

			r.ErrorIs(err, tt.wantErr)
		})
	}
}

// TestApplyNodeRolePoolDefaultsMultiValue extends the D1 pool-sizing rule
// (spec 2026-07-05-09) to the multi-value form: an api-serving node keeps the
// large pool whatever else it runs, a worker-only node gets the small one.
func TestApplyNodeRolePoolDefaultsMultiValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		role             string
		wantMaxOpenConns int
		wantMaxIdleConns int
	}{
		{
			name: "api,jobs keeps the api-sized pool", role: "api,jobs",
			wantMaxOpenConns: dbPoolMaxOpenConnsDefault, wantMaxIdleConns: dbPoolMaxIdleConnsDefault,
		},
		{
			name: "api,checks keeps the api-sized pool", role: "api,checks",
			wantMaxOpenConns: dbPoolMaxOpenConnsDefault, wantMaxIdleConns: dbPoolMaxIdleConnsDefault,
		},
		{
			name: "checks,jobs shrinks to the worker-sized pool", role: "checks,jobs",
			wantMaxOpenConns: dbPoolMaxOpenConnsChecksDefault, wantMaxIdleConns: dbPoolMaxIdleConnsChecksDefault,
		},
		{
			name: "agent keeps the defaults (it opens no database at all)", role: NodeRoleAgent,
			wantMaxOpenConns: dbPoolMaxOpenConnsDefault, wantMaxIdleConns: dbPoolMaxIdleConnsDefault,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			cfg := DatabaseConfig{
				MaxOpenConns: dbPoolMaxOpenConnsDefault,
				MaxIdleConns: dbPoolMaxIdleConnsDefault,
			}
			applyNodeRolePoolDefaults(&cfg, tt.role)

			r.Equal(tt.wantMaxOpenConns, cfg.MaxOpenConns)
			r.Equal(tt.wantMaxIdleConns, cfg.MaxIdleConns)
		})
	}
}

// TestLoad_MultiValueNodeRoleSurvivesEnv is the end-to-end guard on the config
// path: koanf's env loader must hand the comma-separated value to Node.Role
// verbatim (no splitting, no truncation at the comma), and the subsystem gates
// must read it correctly. Uses t.Setenv, which is incompatible with t.Parallel.
func TestLoad_MultiValueNodeRoleSurvivesEnv(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_NODE_ROLE", "api,jobs")

	cfg, err := Load()
	r.NoError(err)
	r.Equal("api,jobs", cfg.Node.Role)
	r.NoError(cfg.validateNodeConfig())
	r.True(cfg.ShouldRunAPI())
	r.True(cfg.ShouldRunJobs())
	r.False(cfg.ShouldRunChecks())
	r.False(cfg.IsAgentMode())
	// An api-serving node keeps the large pool.
	r.Equal(dbPoolMaxOpenConnsDefault, cfg.Database.MaxOpenConns)
}

// TestLoad_LegacySingleValueNodeRolesSurviveEnv re-runs the backward-compat
// table through the real Load() entry point, so a regression anywhere on the
// env → config → gate path (not just in ParseNodeRoles) is caught. Uses
// t.Setenv, which is incompatible with t.Parallel.
func TestLoad_LegacySingleValueNodeRolesSurviveEnv(t *testing.T) {
	tests := []struct {
		role string
		want roleExpectation
	}{
		{role: NodeRoleAll, want: roleExpectation{api: true, jobs: true, checks: true}},
		{role: NodeRoleAPI, want: roleExpectation{api: true}},
		{role: NodeRoleJobs, want: roleExpectation{jobs: true}},
		{role: NodeRoleChecks, want: roleExpectation{checks: true}},
		{role: NodeRoleAgent, want: roleExpectation{agent: true}},
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			r := require.New(t)

			t.Setenv("SP_NODE_ROLE", tt.role)

			cfg, err := Load()
			r.NoError(err)
			r.Equal(tt.role, cfg.Node.Role)
			r.Equal(tt.want.api, cfg.ShouldRunAPI())
			r.Equal(tt.want.jobs, cfg.ShouldRunJobs())
			r.Equal(tt.want.checks, cfg.ShouldRunChecks())
			r.Equal(tt.want.agent, cfg.IsAgentMode())
		})
	}
}

// TestLoad_UnsetNodeRoleDefaultsToAll pins the unset case — by far the most
// widely deployed configuration — end to end: no SP_NODE_ROLE at all still
// means "all", and still starts every subsystem. Uses t.Setenv, which is
// incompatible with t.Parallel.
//
//nolint:paralleltest // reads process env; must not race a sibling t.Setenv
func TestLoad_UnsetNodeRoleDefaultsToAll(t *testing.T) {
	r := require.New(t)

	// t.Setenv has no unset counterpart; SP_NODE_ROLE is not part of the test
	// environment, so guard rather than silently asserting the wrong thing.
	if role, ok := os.LookupEnv("SP_NODE_ROLE"); ok {
		t.Skipf("SP_NODE_ROLE is set in the environment (%q); cannot test the unset default", role)
	}

	cfg, err := Load()
	r.NoError(err)
	r.Equal(NodeRoleAll, cfg.Node.Role)
	r.NoError(cfg.validateNodeConfig())
	r.True(cfg.ShouldRunAPI())
	r.True(cfg.ShouldRunJobs())
	r.True(cfg.ShouldRunChecks())
	r.False(cfg.IsAgentMode())
}

// TestLoad_InvalidNodeRoleFailsValidation proves the loud-failure half: a typo
// reaches Validate() and aborts startup instead of producing a node that
// quietly runs nothing. Uses t.Setenv, which is incompatible with t.Parallel.
func TestLoad_InvalidNodeRoleFailsValidation(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_NODE_ROLE", "api,chekcs")

	cfg, err := Load()
	r.NoError(err)

	err = cfg.Validate()
	r.ErrorIs(err, ErrInvalidNodeRole)
	r.Contains(err.Error(), "chekcs")
}
