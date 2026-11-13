package jobtypes

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

func runWebhookConfig(t *testing.T, cfg *WebhookJobConfig) error {
	t.Helper()

	raw, err := json.Marshal(cfg)
	require.NoError(t, err)

	runner, err := (&WebhookJobDefinition{}).CreateJobRun(raw)
	require.NoError(t, err)

	return runner.Run(t.Context(), &jobdef.JobContext{Logger: slog.New(slog.DiscardHandler)})
}

// The configured headers reach the receiver, on top of the JSON content type.
func TestWebhookJob_SendsConfiguredHeaders(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	got := make(chan http.Header, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got <- req.Header.Clone()

		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	r.NoError(runWebhookConfig(t, &WebhookJobConfig{
		URL:     server.URL,
		Headers: map[string]string{"X-Acme-Token": "s3cret", "X-Acme-Source": "solidping"},
		Body:    map[string]any{"event": "publication.updated"},
	}))

	headers := <-got
	r.Equal("s3cret", headers.Get("X-Acme-Token"))
	r.Equal("solidping", headers.Get("X-Acme-Source"))
	r.Equal("application/json", headers.Get("Content-Type"))
}

// A receiver that cannot be reached is worth a retry; a request that cannot
// even be built is not.
func TestWebhookJob_TransportFailures(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()

	t.Run("receiver unreachable", func(t *testing.T) {
		t.Parallel()

		err := runWebhookConfig(t, &WebhookJobConfig{URL: server.URL})
		require.ErrorContains(t, err, "sending webhook")
		require.True(t, jobdef.IsRetryable(err))
	})

	t.Run("invalid method", func(t *testing.T) {
		t.Parallel()

		err := runWebhookConfig(t, &WebhookJobConfig{URL: server.URL, Method: "NOT A METHOD"})
		require.ErrorContains(t, err, "creating webhook request")
		require.False(t, jobdef.IsRetryable(err))
	})
}

// A purge that fails keeps the events and asks for a retry.
func TestEventsCleanup_DeleteFailureIsRetryable(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	jctx, dbSvc, orgUID := newEventsCleanupTestContext(t, 365)
	ancient := seedEventAt(t, dbSvc, orgUID, time.Now().AddDate(0, 0, -400))
	fault := failQueries(jctx.DB, `"events"`, "DELETE")

	err := (&EventsCleanupJobRun{}).Run(t.Context(), jctx)
	fault.requireFired(t)

	r.ErrorIs(err, context.Canceled)
	r.True(jobdef.IsRetryable(err))
	r.True(eventUIDs(t, dbSvc, orgUID)[ancient], "nothing was deleted")
}

// A stored retention that is not a number is ignored: the configured value
// applies, the events are not kept forever by accident nor purged on a typo.
func TestEventsCleanup_UnparseableStoredRetentionFallsThrough(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	jctx, dbSvc, orgUID := newEventsCleanupTestContext(t, 365)
	r.NoError(dbSvc.SetSystemParameter(t.Context(), string(systemconfig.KeyAuditRetentionDays), "soon", false))

	noEnv := func(string) (string, bool) { return "", false }
	r.Equal(365, resolveAuditRetentionDays(t.Context(), jctx, noEnv))

	ancient := seedEventAt(t, dbSvc, orgUID, time.Now().AddDate(0, 0, -400))
	recent := seedEventAt(t, dbSvc, orgUID, time.Now().Add(-time.Hour))

	// Without a job service the purge still runs; it only cannot re-arm.
	jctx.Services = nil
	r.NoError((&EventsCleanupJobRun{}).Run(t.Context(), jctx))

	remaining := eventUIDs(t, dbSvc, orgUID)
	r.False(remaining[ancient])
	r.True(remaining[recent])
}
