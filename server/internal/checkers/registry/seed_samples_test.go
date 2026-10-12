package registry

import (
	"encoding/json"
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// seededDefaultSamples returns what the startup job seeds on a fresh install
// in the default run mode, with the base URL a local install resolves to so a
// sample built on it would show up as a localhost target.
func seededDefaultSamples() map[checkerdef.CheckType][]checkerdef.CheckSpec {
	opts := &checkerdef.ListSampleOptions{Type: checkerdef.Default, BaseURL: "http://localhost:4000"}
	result := make(map[checkerdef.CheckType][]checkerdef.CheckSpec)

	for checkType, samples := range GetAllSampleConfigs(opts) {
		if seeds := checkerdef.SamplesToSeed(opts, samples); len(seeds) > 0 {
			result[checkType] = seeds
		}
	}

	return result
}

func TestSeededDefaultSamplesAreAFewPublicTypes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// The only types that need no local service and no extra privilege.
	seedTypes := map[checkerdef.CheckType]bool{
		checkerdef.CheckTypeHTTP: true,
		checkerdef.CheckTypeDNS:  true,
		checkerdef.CheckTypeSSL:  true,
		checkerdef.CheckTypeTCP:  true,
		checkerdef.CheckTypeICMP: true,
	}

	seeded := seededDefaultSamples()
	total := 0
	slugs := make(map[string]bool)

	for checkType, seeds := range seeded {
		r.True(seedTypes[checkType], "%s samples must not be seeded on a fresh install", checkType)
		r.LessOrEqual(len(seeds), 3, "%s seeds more than 3 samples", checkType)

		for i := range seeds {
			r.False(slugs[seeds[i].Slug], "duplicate seeded slug %s", seeds[i].Slug)
			slugs[seeds[i].Slug] = true
		}

		total += len(seeds)
	}

	r.GreaterOrEqual(total, 10, "a fresh install should still show a useful dashboard")
	r.LessOrEqual(total, 15, "a fresh install should not be flooded with samples")
}

func TestSeededDefaultSamplesValidate(t *testing.T) {
	t.Parallel()

	for checkType, seeds := range seededDefaultSamples() {
		checker, ok := GetChecker(checkType)
		require.True(t, ok, "no checker for %s", checkType)

		for i := range seeds {
			seed := seeds[i]

			t.Run(seed.Slug, func(t *testing.T) {
				t.Parallel()

				require.NoError(t, checker.Validate(&seed),
					"the startup job silently skips a seed that fails validation")
			})
		}
	}
}

func TestSeededDefaultSamplesTargetPublicIPv4Hosts(t *testing.T) {
	t.Parallel()

	for _, seeds := range seededDefaultSamples() {
		for i := range seeds {
			seed := seeds[i]

			t.Run(seed.Slug, func(t *testing.T) {
				t.Parallel()
				r := require.New(t)

				// Docker's default network is IPv4 only.
				r.NotEqual(string(checkerdef.IPVersionIPv6), seed.Config[checkerdef.IPVersionConfigKey],
					"a seed must not be pinned to IPv6")

				for _, value := range configStrings(t, seed.Config) {
					requirePublicIPv4Target(r, value)
				}
			})
		}
	}
}

// configStrings returns every string leaf of a sample config. It goes through
// JSON so nested structs (assertions, headers) are walked like stored configs.
func configStrings(t *testing.T, config map[string]any) []string {
	t.Helper()

	raw, err := json.Marshal(config)
	require.NoError(t, err)

	var decoded any
	require.NoError(t, json.Unmarshal(raw, &decoded))

	var out []string

	var walk func(node any)
	walk = func(node any) {
		switch value := node.(type) {
		case string:
			out = append(out, value)
		case map[string]any:
			for _, child := range value {
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}
	walk(decoded)

	return out
}

// requirePublicIPv4Target fails when value names a loopback, unspecified or
// private host, or an IPv6 literal, as a whole string, a URL host or a
// host:port.
func requirePublicIPv4Target(r *require.Assertions, value string) {
	lower := strings.ToLower(value)
	for _, local := range []string{"localhost", "127.0.0.1", "0.0.0.0", "::1"} {
		r.NotContains(lower, local, "seed targets a local host: %q", value)
	}

	hosts := []string{value}

	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		hosts = append(hosts, parsed.Hostname())
	}

	if host, _, err := net.SplitHostPort(value); err == nil {
		hosts = append(hosts, host)
	}

	for _, host := range hosts {
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if ip == nil {
			continue
		}

		r.NotNil(ip.To4(), "seed targets an IPv6 literal: %q", value)
		r.False(ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast(),
			"seed targets a non-public address: %q", value)
	}
}

// TestPublicIPv4TargetGuard proves the guard above is not vacuous.
func TestPublicIPv4TargetGuard(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{
		"http://localhost:4000/api/v1/fake",
		"127.0.0.1",
		"[::1]:5432",
		"2606:4700:4700::1111",
		"https://[2606:4700:4700::1111]/",
		"10.0.0.1:53",
	} {
		t.Run(bad, func(t *testing.T) {
			t.Parallel()

			mock := &failureRecorder{}
			requirePublicIPv4Target(require.New(mock), bad)
			require.True(t, mock.failed, "%q should be rejected", bad)
		})
	}

	for _, good := range []string{"https://one.one.one.one", "8.8.8.8:53", "1.1.1.1", "google.com"} {
		mock := &failureRecorder{}
		requirePublicIPv4Target(require.New(mock), good)
		require.False(t, mock.failed, "%q should be accepted", good)
	}
}

// failureRecorder is a require.TestingT that records a failure instead of
// stopping the test, so the guard can be exercised against bad input.
type failureRecorder struct {
	failed bool
}

func (f *failureRecorder) Errorf(string, ...any) { f.failed = true }

func (f *failureRecorder) FailNow() { f.failed = true }
