package discovery

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// fakeDockerAPI starts an httptest server that answers the Docker API calls
// ListContainers makes: the version-negotiation ping and GET /containers/json.
// The path is matched by suffix so the client's "/v1.xx" version prefix (added
// during negotiation) does not need to be predicted.
func fakeDockerAPI(t *testing.T, summaries []container.Summary) string {
	t.Helper()

	// Marshal up front so the handler (running on the server goroutine) does no
	// assertions — testifylint forbids require inside an http handler.
	body, err := json.Marshal(summaries)
	require.NoError(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.45")
			w.Header().Set("Ostype", "linux")
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	// http://127.0.0.1:PORT → tcp://127.0.0.1:PORT for the Docker client.
	return "tcp://" + strings.TrimPrefix(srv.URL, "http://")
}

func TestListContainersMapsSummaries(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	endpoint := fakeDockerAPI(t, []container.Summary{
		{
			ID:     "abc123def456",
			Names:  []string{"/web"},
			Image:  "nginx:latest",
			State:  "running",
			Status: "Up 2 hours (healthy)",
			Ports: []container.PortSummary{
				{PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
				{PrivatePort: 9000, PublicPort: 0, Type: "tcp"}, // exposed-only
			},
		},
		{
			ID:     "fed654cba321",
			Names:  []string{"/db"},
			Image:  "postgres:16",
			State:  "running",
			Status: "Up 5 minutes",
		},
	})

	got, err := ListContainers(t.Context(), endpoint, 5*time.Second)
	r.NoError(err)
	r.Len(got, 2)

	web := got[0]
	r.Equal("abc123def456", web.ID)
	r.Equal("web", web.Name, "leading slash must be stripped")
	r.Equal("nginx:latest", web.Image)
	r.Equal("running", web.State)
	r.Equal(HealthHealthy, web.HealthStatus)
	r.Len(web.Ports, 2)
	r.Equal(uint16(80), web.Ports[0].PrivatePort)
	r.Equal(uint16(8080), web.Ports[0].PublicPort)
	r.Equal(uint16(0), web.Ports[1].PublicPort, "exposed-only port has no public port")

	db := got[1]
	r.Equal("db", db.Name)
	r.Empty(db.HealthStatus, "no health suffix means empty health status")
}

func TestListContainersEmpty(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	endpoint := fakeDockerAPI(t, []container.Summary{})
	got, err := ListContainers(t.Context(), endpoint, 5*time.Second)
	r.NoError(err)
	r.Empty(got)
}

func TestListContainersUnreachableEndpointErrors(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// 127.0.0.1:1 is reserved/unused — the connection is refused, so the engine
	// returns an error (which the job logs and skips per endpoint).
	_, err := ListContainers(t.Context(), "tcp://127.0.0.1:1", 2*time.Second)
	r.Error(err)
}

func TestParseHealthStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status string
		want   string
	}{
		{"Up 2 hours (" + HealthHealthy + ")", HealthHealthy},
		{"Up 5 minutes (" + HealthUnhealthy + ")", HealthUnhealthy},
		{"Up 1 second (health: " + HealthStarting + ")", HealthStarting},
		{"Up 3 days", ""},
		{"Exited (0) 2 hours ago", ""}, // "(0)" is not a health word
		{"", ""},
	}

	for _, tc := range tests {
		t.Run(tc.status, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, parseHealthStatus(tc.status))
		})
	}
}

// A unix:// discovery endpoint the client would dial itself must be a listed
// Docker socket (spec 2026-11-27-03): an unlisted one is refused before any
// dial.
func TestListContainersRefusesAnUnlistedSocket(t *testing.T) {
	t.Parallel()

	dir, err := os.MkdirTemp("", "spdisc")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sock := filepath.Join(dir, "other.sock")

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	var accepted atomic.Int32

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}

			accepted.Add(1)

			_ = conn.Close()
		}
	}()

	_, err = ListContainers(t.Context(), "unix://"+sock, time.Second)
	require.ErrorIs(t, err, checkerdef.ErrLocalSocketNotAllowed)
	require.ErrorContains(t, err, checkerdef.DockerSocketSetting)
	require.Zero(t, accepted.Load())

	// Positive control: listed, the socket is dialed (nothing speaks the
	// Docker API on it, so the listing itself fails).
	ctx := checkerdef.WithDockerSocketAllowlist(t.Context(), []string{sock})
	_, err = ListContainers(ctx, "unix://"+sock, time.Second)
	require.Error(t, err)
	require.NotErrorIs(t, err, checkerdef.ErrLocalSocketNotAllowed)
	require.Eventually(t, func() bool { return accepted.Load() > 0 }, time.Second, 10*time.Millisecond)
}
