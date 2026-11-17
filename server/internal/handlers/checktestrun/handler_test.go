package checktestrun

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/aichecks"
	"github.com/fclairamb/solidping/server/internal/config"
)

type neverRunner struct{ called bool }

func (n *neverRunner) Test(
	_ context.Context, _ string, _, _ map[string]string, _ time.Duration,
) (*aichecks.RunResult, error) {
	n.called = true

	return &aichecks.RunResult{}, nil
}

func TestTestRunTooManyInProgress(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	runner := &neverRunner{}
	handler := NewHandler(runner, &config.Config{})

	for range maxConcurrent {
		handler.slots <- struct{}{}
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/orgs/acme/checks/test-run",
		strings.NewReader(`{"type":"js","config":{"script":"return { status: 'up' };"}}`))
	rec := httptest.NewRecorder()

	r.NoError(handler.TestRun(rec, req))
	r.Equal(http.StatusTooManyRequests, rec.Code)
	r.Equal("5", rec.Header().Get("Retry-After"))
	r.False(runner.called, "no script runs when every slot is busy")
}
