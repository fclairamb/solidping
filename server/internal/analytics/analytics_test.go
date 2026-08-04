package analytics_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/analytics"
	"github.com/fclairamb/solidping/server/internal/config"
)

// recorder is a fake PostHog ingestion endpoint. Every test points its client
// at this server, so "did analytics reach the network" is directly observable
// rather than inferred.
type recorder struct {
	mu       sync.Mutex
	requests int
	bodies   []string
}

func newRecorder(t *testing.T) (*recorder, string) {
	t.Helper()

	rec := &recorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		rec.mu.Lock()
		rec.requests++
		rec.bodies = append(rec.bodies, string(body))
		rec.mu.Unlock()

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":1}`))
	}))
	t.Cleanup(server.Close)

	return rec, server.URL
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.requests
}

func (r *recorder) allBodies() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return strings.Join(r.bodies, "\n")
}

// TestDisabledClientNeverTouchesTheNetwork is the core negative test: for every
// shape of "not configured", capturing events and closing the client must
// produce ZERO HTTP requests to the configured PostHog host.
func TestDisabledClientNeverTouchesTheNetwork(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		make func(host string) config.PostHogConfig
	}{
		{
			name: "zero value",
			make: func(host string) config.PostHogConfig {
				return config.PostHogConfig{Host: host}
			},
		},
		{
			name: "enabled but no key (the self-hosted default)",
			make: func(host string) config.PostHogConfig {
				return config.PostHogConfig{Enabled: true, Host: host}
			},
		},
		{
			name: "key present but kill switch off",
			make: func(host string) config.PostHogConfig {
				return config.PostHogConfig{Enabled: false, Host: host, ProjectAPIKey: "phc_key"}
			},
		},
		{
			name: "whitespace-only key",
			make: func(host string) config.PostHogConfig {
				return config.PostHogConfig{Enabled: true, Host: host, ProjectAPIKey: "  \t "}
			},
		},
		{
			name: "personal key set but no project key",
			make: func(host string) config.PostHogConfig {
				return config.PostHogConfig{Enabled: true, Host: host, PersonalAPIKey: "phx_key"}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			rec, host := newRecorder(t)
			client := analytics.New(tc.make(host))

			r.False(client.Enabled(), "an unconfigured deployment must get the no-op client")

			for _, name := range []string{
				analytics.EventOrgCreated,
				analytics.EventUserSignedUp,
				analytics.EventCheckCreated,
				analytics.EventIntegrationConnected,
				analytics.EventStatusPagePublished,
			} {
				client.Capture(context.Background(), analytics.Event{
					Name:    name,
					OrgUID:  "11111111-1111-1111-1111-111111111111",
					UserUID: "22222222-2222-2222-2222-222222222222",
				})
			}

			r.NoError(client.Close(context.Background()))

			// Give any (hypothetical) background flusher a chance to misbehave.
			time.Sleep(200 * time.Millisecond)

			r.Equal(0, rec.count(), "a disabled analytics client must issue no HTTP request at all")
		})
	}
}

// TestProcessWideDefaultLifecycle proves the process-wide default — what every
// call site actually uses — is inert until something configures it.
//
// The two halves live in one test function rather than two because both mutate
// the package-level default; splitting them would make the parallel runs race.
func TestProcessWideDefaultLifecycle(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rec, host := newRecorder(t)

	// Explicitly install the client an unconfigured deployment would get.
	analytics.SetDefault(analytics.New(config.PostHogConfig{Enabled: true, Host: host}))
	t.Cleanup(func() { analytics.SetDefault(nil) })

	r.False(analytics.Default().Enabled())

	analytics.Capture(context.Background(), analytics.Event{
		Name:   analytics.EventCheckCreated,
		OrgUID: "11111111-1111-1111-1111-111111111111",
	})
	r.NoError(analytics.Close(context.Background()))

	time.Sleep(200 * time.Millisecond)
	r.Equal(0, rec.count())

	// Resetting to nil must land back on the no-op, never on a nil client.
	analytics.SetDefault(nil)
	r.NotNil(analytics.Default())
	r.False(analytics.Default().Enabled())
}

// TestConfiguredClientCaptures is the positive control for the negative tests
// above: with a project key present the very same calls DO reach the endpoint,
// carrying the pseudonymous distinct id and nothing else.
func TestConfiguredClientCaptures(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rec, host := newRecorder(t)

	client := analytics.New(config.PostHogConfig{
		Enabled:       true,
		Host:          host,
		ProjectAPIKey: "phc_test_key",
	})
	r.True(client.Enabled())

	client.Capture(context.Background(), analytics.Event{
		Name:       analytics.EventCheckCreated,
		OrgUID:     "11111111-1111-1111-1111-111111111111",
		UserUID:    "22222222-2222-2222-2222-222222222222",
		Properties: map[string]any{"checkType": "http"},
	})

	// Close flushes synchronously.
	r.NoError(client.Close(context.Background()))

	r.Positive(rec.count(), "a configured client must actually send the event")

	body := rec.allBodies()
	r.Contains(body, analytics.EventCheckCreated)
	r.Contains(body, "org:11111111-1111-1111-1111-111111111111/user:22222222-2222-2222-2222-222222222222")
	r.Contains(body, "http")
}

// TestDistinctIDSchemeMatchesFrontend pins the exact id format. The dashboard's
// src/lib/analytics.ts builds the same string; if this changes, that must too,
// or browser and server sessions stop stitching.
func TestDistinctIDSchemeMatchesFrontend(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("org:org-uid/user:user-uid", analytics.DistinctID("org-uid", "user-uid"))
	r.Equal("org:org-uid", analytics.DistinctID("org-uid", ""))
	r.Equal("user:user-uid", analytics.DistinctID("", "user-uid"))
	r.Equal("anonymous", analytics.DistinctID("", ""))
}

// TestNoopCaptureIsSafeWithEmptyEvent guards against a panic on a malformed
// call — analytics must never be able to fail a request path.
func TestNoopCaptureIsSafeWithEmptyEvent(t *testing.T) {
	t.Parallel()

	client := analytics.NewNoop()
	client.Capture(context.Background(), analytics.Event{})
	require.NoError(t, client.Close(context.Background()))
}
