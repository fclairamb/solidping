package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/dbfault"
)

// errSchemaGone is the incident's error: the jobs table vanished under a live
// process.
var errSchemaGone = errors.New("SQL logic error: no such table: jobs (1)")

// errDroppedConnection is an ordinary outage: it must not change health.
var errDroppedConnection = errors.New("dial tcp: connection refused")

func newHealthServer() *Server {
	return &Server{
		config:  &config.Config{Node: config.NodeConfig{Role: "all"}},
		dbFault: dbfault.NewLatch(slog.New(slog.DiscardHandler)),
	}
}

func getHealth(t *testing.T, srv *Server) (int, HealthResponse) {
	t.Helper()
	r := require.New(t)

	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodGet, "/api/mgmt/health", http.NoBody,
	)
	w := httptest.NewRecorder()
	r.NoError(srv.healthCheck(w, req))

	var resp HealthResponse
	r.NoError(json.Unmarshal(w.Body.Bytes(), &resp))

	return w.Code, resp
}

// TestStructuralFaultCancelsTheServerContext proves the wiring between the
// latch and the process lifecycle: the context Start runs on is canceled, which
// is what unwinds the HTTP server and the runners, and the fault comes back as
// an error so the process exits non-zero and the supervisor restarts it.
func TestStructuralFaultCancelsTheServerContext(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newHealthServer()

	ctx, stop := srv.armDatabaseFaultShutdown(context.Background())
	defer stop()

	r.NoError(ctx.Err(), "healthy: the server keeps running")
	r.NoError(srv.dbFault.Err())

	// A transient error must not take the process down.
	r.False(srv.dbFault.Report(ctx, errDroppedConnection))
	r.NoError(ctx.Err())

	r.True(srv.dbFault.Report(ctx, errSchemaGone))

	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		r.Fail("a structural fault must cancel the server context")
	}

	r.ErrorContains(srv.dbFault.Err(), "undefined_table",
		"Start returns this, so the exit code is non-zero")
}

// TestHealthReportsStructuralDatabaseFault covers the exact gap the incident
// exposed: a process answering "healthy" while every database query fails.
func TestHealthReportsStructuralDatabaseFault(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newHealthServer()

	code, resp := getHealth(t, srv)
	r.Equal(http.StatusOK, code)
	r.Equal("ok", resp.Status)
	r.Empty(resp.Fault)

	// A transient error leaves health alone — an ordinary outage is not this.
	r.False(srv.dbFault.Report(t.Context(), errDroppedConnection))

	code, resp = getHealth(t, srv)
	r.Equal(http.StatusOK, code)
	r.Equal("ok", resp.Status)

	// A structural fault flips it, for the whole shutdown window.
	r.True(srv.dbFault.Report(t.Context(), errSchemaGone))

	code, resp = getHealth(t, srv)
	r.Equal(http.StatusServiceUnavailable, code)
	r.Equal("unhealthy", resp.Status)
	r.Equal("undefined_table", resp.Fault)
	r.NotNil(resp.Node)
	r.Equal("all", resp.Node.Role)
}
