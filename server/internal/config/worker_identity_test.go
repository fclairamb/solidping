package config

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// hostnameNeverCalled fails the test if the hostname is consulted. It is what
// proves the SP_NODE_NAME override short-circuits the os.Hostname() read
// entirely rather than merely overwriting its result.
func hostnameNeverCalled(t *testing.T) func() (string, error) {
	t.Helper()

	return func() (string, error) {
		t.Fatal("os.Hostname() must not be consulted when SP_NODE_NAME is set")

		return "", nil
	}
}

func staticHostname(name string) func() (string, error) {
	return func() (string, error) { return name, nil }
}

// TestResolveWorkerIdentity_Override: an explicit SP_NODE_NAME is used verbatim
// for both slug and name, with no truncation, no case folding, and no hostname
// read.
func TestResolveWorkerIdentity_Override(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		override string
	}{
		{"short slug", "solidping-eu2"},
		{"exactly at the 21-char limit", "abcdefghij-klmnopqrst"},
		{"longer than the hostname cut", "solidping-checks-us1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			id := resolveWorkerIdentity(tc.override, hostnameNeverCalled(t))

			r.Equal(tc.override, id.Slug)
			r.Equal(tc.override, id.Name)
			r.True(id.Overridden)
			r.False(id.Truncated)
			r.Empty(id.Hostname)
			r.NoError(id.Validate())
		})
	}
}

// TestResolveWorkerIdentity_HostnameFallback: with no override, the historic
// behavior is preserved byte-for-byte — 15-char truncation, lowercased slug,
// raw-case name, and "unknown" when os.Hostname() errors.
func TestResolveWorkerIdentity_HostnameFallback(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		hostnameFn    func() (string, error)
		wantSlug      string
		wantWorkName  string
		wantTruncated bool
	}{
		{
			name:         "short hostname passes through lowercased",
			hostnameFn:   staticHostname("Worker-EU2"),
			wantSlug:     "worker-eu2",
			wantWorkName: "Worker-EU2",
		},
		{
			name:         "exactly 15 chars is not truncated",
			hostnameFn:   staticHostname("abcdefghijklmno"),
			wantSlug:     "abcdefghijklmno",
			wantWorkName: "abcdefghijklmno",
		},
		{
			name:          "16 chars is cut to 15",
			hostnameFn:    staticHostname("solidping-checks-eu2-abcde"),
			wantSlug:      "solidping-check",
			wantWorkName:  "solidping-check",
			wantTruncated: true,
		},
		{
			name:          "truncation lowercases too",
			hostnameFn:    staticHostname("SolidPing-Checks-US1"),
			wantSlug:      "solidping-check",
			wantWorkName:  "SolidPing-Check",
			wantTruncated: true,
		},
		{
			name:         "hostname error falls back to unknown",
			hostnameFn:   func() (string, error) { return "", os.ErrNotExist },
			wantSlug:     unknownHostname,
			wantWorkName: unknownHostname,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			id := resolveWorkerIdentity("", tc.hostnameFn)

			r.Equal(tc.wantSlug, id.Slug)
			r.Equal(tc.wantWorkName, id.Name)
			r.False(id.Overridden)
			r.Equal(tc.wantTruncated, id.Truncated)
			r.NoError(id.Validate())
		})
	}
}

// TestWorkerIdentityValidate_Invalid: every slug the database CHECK constraint
// would reject is caught at config-validation time, with a message that names
// the offending value and SP_NODE_NAME as the fix — never an opaque
// SQLSTATE=23514 at INSERT time.
func TestWorkerIdentityValidate_Invalid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		override string
		hostname string
		wantIn   []string
	}{
		{
			name:     "override with dots",
			override: "eu2.example.com",
			wantIn:   []string{"eu2.example.com", "SP_NODE_NAME"},
		},
		{
			name:     "override too long",
			override: "solidping-checks-eu2-far-too-long",
			wantIn:   []string{"SP_NODE_NAME"},
		},
		{
			name:     "override with a leading digit",
			override: "1solidping",
			wantIn:   []string{"1solidping", "SP_NODE_NAME"},
		},
		{
			name:     "override with uppercase",
			override: "Worker-EU2",
			wantIn:   []string{"Worker-EU2", "SP_NODE_NAME"},
		},
		{
			name:     "override too short",
			override: "ab",
			wantIn:   []string{"SP_NODE_NAME"},
		},
		{
			// The hostNetwork failure mode: the host UTS namespace hands the
			// pod a dotted node name.
			name:     "hostname with dots (kubernetes hostNetwork)",
			hostname: "eu2.example.com",
			wantIn:   []string{"eu2.example.com", "SP_NODE_NAME", "hostname"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			hostnameFn := hostnameNeverCalled(t)
			if tc.override == "" {
				hostnameFn = staticHostname(tc.hostname)
			}

			err := resolveWorkerIdentity(tc.override, hostnameFn).Validate()
			r.Error(err)
			r.ErrorIs(err, ErrInvalidWorkerSlug)

			for _, want := range tc.wantIn {
				r.Containsf(err.Error(), want, "error message must mention %q: %s", want, err)
			}
		})
	}
}

// TestWorkerIdentityWarnIfTruncated proves a truncated hostname produces a WARN
// naming the resulting slug (silent slug collisions become visible), and that a
// non-truncated or overridden identity stays quiet.
func TestWorkerIdentityWarnIfTruncated(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		identity WorkerIdentity
		wantWarn bool
	}{
		{
			name:     "truncated hostname warns",
			identity: resolveWorkerIdentity("", staticHostname("solidping-checks-eu2-abcde")),
			wantWarn: true,
		},
		{
			name:     "short hostname stays quiet",
			identity: resolveWorkerIdentity("", staticHostname("worker-eu2")),
		},
		{
			name:     "override stays quiet",
			identity: resolveWorkerIdentity("solidping-eu2", func() (string, error) { return "", nil }),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

			tc.identity.WarnIfTruncated(context.Background(), logger)

			out := buf.String()
			if !tc.wantWarn {
				r.Empty(out)

				return
			}

			r.Contains(out, "level=WARN")
			r.Contains(out, "slug=solidping-check")
			r.Contains(out, "SP_NODE_NAME")
		})
	}
}

// TestSPNodeNameBindsThroughLoad is the skeptical check on koanf reachability:
// `node.name` is a single-word path under the SP_ prefix, so the env provider
// should reach it with no manual reader — but this repo has a history of
// multi-word SP_* keys silently not binding, so it is proven rather than
// assumed.
func TestSPNodeNameBindsThroughLoad(t *testing.T) {
	t.Setenv("SP_NODE_NAME", "solidping-eu2")

	r := require.New(t)

	cfg, err := Load()
	r.NoError(err)
	r.Equal("solidping-eu2", cfg.Node.Name)
	r.Equal("solidping-eu2", cfg.WorkerIdentity().Slug)
	r.Equal("solidping-eu2", cfg.WorkerIdentity().Name)
	r.True(cfg.WorkerIdentity().Overridden)

	// It must also be advertised as recognized, otherwise the startup env check
	// would flag a variable that in fact binds.
	r.Contains(RecognizedEnvVars(), "SP_NODE_NAME")
}

// TestValidate_WorkerIdentity wires the identity check into Config.Validate:
// nodes that register a `workers` row fail fast on a bad slug, and an API-only
// node (which registers nothing) is unaffected.
func TestValidate_WorkerIdentity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		role    string
		nodName string
		wantErr bool
	}{
		{name: "checks role rejects a dotted name", role: NodeRoleChecks, nodName: "eu2.example.com", wantErr: true},
		{name: "jobs role rejects a dotted name", role: NodeRoleJobs, nodName: "eu2.example.com", wantErr: true},
		{name: "all role rejects a dotted name", role: NodeRoleAll, nodName: "eu2.example.com", wantErr: true},
		{name: "checks role accepts a valid name", role: NodeRoleChecks, nodName: "solidping-eu2"},
		{name: "api role never registers a worker", role: NodeRoleAPI, nodName: "eu2.example.com"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			cfg := validBaseConfig()
			cfg.Node.Role = tc.role
			cfg.Node.Name = tc.nodName
			if tc.role == NodeRoleChecks {
				cfg.Node.Region = "eu"
			}

			err := cfg.Validate()
			if tc.wantErr {
				r.ErrorIs(err, ErrInvalidWorkerSlug)
				r.Contains(err.Error(), "SP_NODE_NAME")

				return
			}

			r.NoError(err)
		})
	}
}

// TestWorkerSlugPatternMatchesMigration guards the copy of the CHECK constraint
// against drift: the pattern here must stay literally identical to the one in
// the initial Postgres migration, or startup validation would start accepting
// slugs the database still rejects.
func TestWorkerSlugPatternMatchesMigration(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal(`^[a-z][a-z0-9-]{2,20}$`, WorkerSlugPattern)
	r.NotContains(WorkerSlugPattern, " ")
}
