package config

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
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
// raw-case name, and "unknown" when os.Hostname() errors. It also asserts the
// spec's "fixed point" property: every hostname here already matches
// WorkerSlugPattern once lowercased/truncated, so sanitization must be a
// no-op (Sanitized false, slug unchanged) — no existing deployment's slug
// moves because of this change.
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
			r.False(id.Sanitized, "an already-valid hostname must be a fixed point of sanitization")
			r.NoError(id.Validate())
		})
	}
}

// TestResolveWorkerIdentity_HostnameSanitization covers the bug this spec
// fixes: a hostname-derived slug with characters outside [a-z0-9-] (dots
// being the killer case — .lan/.local/.home suffixes are the default on
// macOS/LAN machines) is sanitized into a valid slug instead of making the
// worker refuse to start. Name stays the human-readable, original-case,
// truncated hostname — only Slug is sanitized.
func TestResolveWorkerIdentity_HostnameSanitization(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		hostname     string
		wantSlug     string
		wantWorkName string
	}{
		{
			name:         "dots substituted with dashes, not stripped",
			hostname:     "Host-002.lan",
			wantSlug:     "host-002-lan",
			wantWorkName: "Host-002.lan",
		},
		{
			name:         ".local suffix",
			hostname:     "my-mac.local",
			wantSlug:     "my-mac-local",
			wantWorkName: "my-mac.local",
		},
		{
			name:         ".home suffix",
			hostname:     "router.home",
			wantSlug:     "router-home",
			wantWorkName: "router.home",
		},
		{
			name:         "consecutive illegal characters collapse to one dash",
			hostname:     "host..lan",
			wantSlug:     "host-lan",
			wantWorkName: "host..lan",
		},
		{
			name:         "underscore substituted",
			hostname:     "my_host",
			wantSlug:     "my-host",
			wantWorkName: "my_host",
		},
		{
			name:         "leading dot trimmed after substitution",
			hostname:     ".hidden-host",
			wantSlug:     "hidden-host",
			wantWorkName: ".hidden-host",
		},
		{
			// The hostNetwork failure mode this spec fixes: the host UTS
			// namespace hands the pod a dotted node name, and it used to be a
			// hard startup failure — see TestWorkerIdentityValidate_Invalid
			// for the still-pathological residue cases (e.g. a leading digit).
			name:         "kubernetes hostNetwork dotted hostname now boots",
			hostname:     "eu2.example.com",
			wantSlug:     "eu2-example-com",
			wantWorkName: "eu2.example.com",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			id := resolveWorkerIdentity("", staticHostname(tc.hostname))

			r.Equal(tc.wantSlug, id.Slug)
			r.Equal(tc.wantWorkName, id.Name, "Name must stay the original-case hostname, unsanitized")
			r.Equal(tc.hostname, id.Hostname)
			r.False(id.Overridden)
			r.True(id.Sanitized)
			r.NoError(id.Validate())
		})
	}
}

// TestResolveWorkerIdentity_AlreadyValidHostnameIsFixedPoint proves the "no
// existing deployment changes slug" property by construction rather than by a
// hand-picked sample that happens not to exercise it: WorkerSlugPattern
// permits trailing dashes and internal dash runs, so a hostname whose
// (lowercased, truncated) form already matches the pattern must come out of
// sanitizeHostnameSlug byte-identical — collapsing/trimming must never touch
// a slug that already validates today, or an upgrade silently orphans the
// existing `workers` row under a new slug.
func TestResolveWorkerIdentity_AlreadyValidHostnameIsFixedPoint(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		hostname string
	}{
		{
			// 15-char truncation landing mid-word, leaving a trailing dash —
			// WorkerSlugPattern allows it, so it must survive untouched.
			name:     "truncation lands on a trailing dash",
			hostname: "my-worker-node-01",
		},
		{
			name:     "truncation lands on a trailing dash (2)",
			hostname: "eu-west-prod-1-a",
		},
		{
			name:     "truncation lands on a trailing dash (3)",
			hostname: "worker-alpha-1-b",
		},
		{
			// A genuinely double-dashed hostname — unusual, but the CHECK
			// constraint allows it and it may already be registered.
			name:     "consecutive internal dashes, already valid",
			hostname: "db--primary--x1",
		},
		{
			name:     "already-clean short hostname",
			hostname: "worker-eu2",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			truncated := tc.hostname
			if len(truncated) > WorkerHostnameMaxLen {
				truncated = truncated[:WorkerHostnameMaxLen]
			}

			todaySlug := strings.ToLower(truncated)
			r.Regexp(workerSlugRegexp, todaySlug, "test setup: case must already be valid today")

			id := resolveWorkerIdentity("", staticHostname(tc.hostname))

			r.Equal(todaySlug, id.Slug, "an already-valid slug must be byte-identical after sanitization")
			r.False(id.Sanitized, "no rewrite happened, so Sanitized must stay false — no WARN for a host that works today")
		})
	}
}

// TestSanitizeHostnameSlug_FixedPointProperty states the spec's fixed-point
// property directly: for any lowercased, truncated hostname that already
// matches WorkerSlugPattern, sanitizeHostnameSlug must return it unchanged.
// It also keeps a positive control (a hostname that genuinely needs
// sanitizing) in the same table, so the assertion below would fail if the
// early-return swallowed everything instead of only the already-valid cases.
func TestSanitizeHostnameSlug_FixedPointProperty(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	candidates := []string{
		"my-worker-node-", // trailing dash, valid today
		"db--primary--x1", // internal dash run, valid today
		"worker-eu2",      // plain, valid today
		"host-002.lan",    // needs substitution — the positive control
	}

	sawAlreadyValid := false
	sawNeedsSanitizing := false

	for _, hostname := range candidates {
		got := sanitizeHostnameSlug(hostname)

		if workerSlugRegexp.MatchString(hostname) {
			sawAlreadyValid = true
			r.Equal(hostname, got, "fixed point: %q already matches WorkerSlugPattern", hostname)
		} else {
			sawNeedsSanitizing = true
			r.NotEqual(hostname, got, "positive control: %q must actually be rewritten", hostname)
		}
	}

	r.True(sawAlreadyValid, "test setup: must exercise the fixed-point branch")
	r.True(sawNeedsSanitizing, "test setup: must exercise the positive control")
}

// TestResolveWorkerIdentity_OverrideNeverSanitized proves sanitization only
// ever applies to the hostname-derived path: an explicit SP_NODE_NAME with
// illegal characters is passed through verbatim (Sanitized stays false) and
// still fails hard on Validate — operator intent must not be silently
// rewritten.
func TestResolveWorkerIdentity_OverrideNeverSanitized(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	id := resolveWorkerIdentity("eu2.example.com", hostnameNeverCalled(t))

	r.Equal("eu2.example.com", id.Slug)
	r.True(id.Overridden)
	r.False(id.Sanitized)
	r.ErrorIs(id.Validate(), ErrInvalidWorkerSlug)
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
			// Pathological residue: sanitization substitutes the dots but the
			// result still starts with a digit, which the CHECK constraint
			// (and WorkerSlugPattern) rejects regardless of origin.
			name:     "hostname sanitizes to a leading digit",
			hostname: "10.0.0.1",
			wantIn:   []string{"10.0.0.1", "SP_NODE_NAME", "hostname"},
		},
		{
			// Pathological residue: nothing survives collapsing/trimming, so
			// the derived slug is empty.
			name:     "hostname sanitizes to empty",
			hostname: "...",
			wantIn:   []string{"...", "SP_NODE_NAME", "hostname"},
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

// TestWorkerIdentityWarnIfSanitized proves a hostname whose illegal
// characters had to be substituted produces a WARN naming the resulting slug
// (in the spirit of WarnIfTruncated), and that a hostname needing no
// substitution or an overridden identity stays quiet.
func TestWorkerIdentityWarnIfSanitized(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		identity WorkerIdentity
		wantWarn bool
	}{
		{
			name:     "dotted hostname warns",
			identity: resolveWorkerIdentity("", staticHostname("host-002.lan")),
			wantWarn: true,
		},
		{
			name:     "already-clean hostname stays quiet",
			identity: resolveWorkerIdentity("", staticHostname("worker-eu2")),
		},
		{
			name:     "override stays quiet even with illegal characters",
			identity: resolveWorkerIdentity("eu2.example.com", func() (string, error) { return "", nil }),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

			tc.identity.WarnIfSanitized(context.Background(), logger)

			out := buf.String()
			if !tc.wantWarn {
				r.Empty(out)

				return
			}

			r.Contains(out, "level=WARN")
			r.Contains(out, "slug=host-002-lan")
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
