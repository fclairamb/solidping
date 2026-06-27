package discovery

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/require"
)

// fakeDockerAPI starts an httptest server that answers the Docker API calls
// ListContainers makes: the version-negotiation ping and GET /containers/json.
// The path is matched by suffix so the client's "/v1.xx" version prefix (added
// during negotiation) does not need to be predicted.
func fakeDockerAPI(t *testing.T, summaries []container.Summary) string {
	t.Helper()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.45")
			w.Header().Set("Ostype", "linux")
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(summaries)
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
			Ports: []container.Port{
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
	r.Equal("healthy", web.HealthStatus)
	r.Len(web.Ports, 2)
	r.Equal(uint16(80), web.Ports[0].PrivatePort)
	r.Equal(uint16(8080), web.Ports[0].PublicPort)
	r.Equal(uint16(0), web.Ports[1].PublicPort, "exposed-only port has no public port")

	db := got[1]
	r.Equal("db", db.Name)
	r.Equal("", db.HealthStatus, "no health suffix means empty health status")
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
		{"Up 2 hours (healthy)", "healthy"},
		{"Up 5 minutes (unhealthy)", "unhealthy"},
		{"Up 1 second (health: starting)", "starting"},
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
