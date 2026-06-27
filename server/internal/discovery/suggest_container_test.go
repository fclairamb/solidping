package discovery

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSuggestContainerChecksGroupedRows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		ports     []ContainerPort
		wantTypes []string
	}{
		{
			name:      "no published ports yields only the docker check",
			ports:     nil,
			wantTypes: []string{"docker"},
		},
		{
			name:      "expose-only (no public port) yields only docker",
			ports:     []ContainerPort{{PrivatePort: 8080, PublicPort: 0, Type: "tcp"}},
			wantTypes: []string{"docker"},
		},
		{
			name:      "http on 80",
			ports:     []ContainerPort{{PrivatePort: 80, PublicPort: 8081, Type: "tcp"}},
			wantTypes: []string{"docker", "http"},
		},
		{
			name:      "http on 8080",
			ports:     []ContainerPort{{PrivatePort: 8080, PublicPort: 18080, Type: "tcp"}},
			wantTypes: []string{"docker", "http"},
		},
		{
			name:      "https on 443",
			ports:     []ContainerPort{{PrivatePort: 443, PublicPort: 8443, Type: "tcp"}},
			wantTypes: []string{"docker", "http"},
		},
		{
			name:      "https on 8443",
			ports:     []ContainerPort{{PrivatePort: 8443, PublicPort: 18443, Type: "tcp"}},
			wantTypes: []string{"docker", "http"},
		},
		{
			name:      "other port -> tcp",
			ports:     []ContainerPort{{PrivatePort: 5432, PublicPort: 15432, Type: "tcp"}},
			wantTypes: []string{"docker", "tcp"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			in := ContainerSuggestInput{
				ContainerID:  "abc123",
				Name:         "web",
				Image:        "nginx:latest",
				State:        "running",
				HealthStatus: "healthy",
				DockerHost:   "unix:///var/run/docker.sock",
				Ports:        tc.ports,
			}

			got := SuggestContainerChecks(in)
			r.Len(got, len(tc.wantTypes))

			for i, wantType := range tc.wantTypes {
				r.Equal(wantType, got[i].Type)
				// Every row of the group shares the container ID as the group key.
				r.Equal("abc123", got[i].GroupKey)
				r.Equal("web", got[i].GroupLabel)
				r.NotEmpty(got[i].Name)
				r.NotEmpty(got[i].Slug)
				r.NotEmpty(got[i].Config)

				// Metadata is denormalized and identical across the group's rows.
				var meta struct {
					Image        string `json:"image"`
					State        string `json:"state"`
					HealthStatus string `json:"healthStatus"`
					DockerHost   string `json:"dockerHost"`
				}
				r.NoError(json.Unmarshal(got[i].Metadata, &meta))
				r.Equal("nginx:latest", meta.Image)
				r.Equal("running", meta.State)
				r.Equal("healthy", meta.HealthStatus)
				r.Equal("unix:///var/run/docker.sock", meta.DockerHost)
			}
		})
	}
}

func TestSuggestContainerChecksDockerConfigAlwaysPresent(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	got := SuggestContainerChecks(ContainerSuggestInput{
		ContainerID: "id-1",
		Name:        "db",
		DockerHost:  "tcp://10.0.0.5:2375",
	})
	r.Len(got, 1)
	r.Equal("docker", got[0].Type)

	var cfg struct {
		Host          string `json:"host"`
		ContainerName string `json:"containerName"`
	}
	r.NoError(json.Unmarshal(got[0].Config, &cfg))
	r.Equal("tcp://10.0.0.5:2375", cfg.Host)
	r.Equal("db", cfg.ContainerName)
}

func TestSuggestContainerChecksPublishedPortUsesHostAddress(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// A tcp:// endpoint: the published-port check targets the endpoint host, not
	// localhost, with the host-published port (not the container-side port).
	got := SuggestContainerChecks(ContainerSuggestInput{
		ContainerID: "id-2",
		Name:        "api",
		DockerHost:  "tcp://10.0.0.5:2375",
		Ports: []ContainerPort{
			{PrivatePort: 80, PublicPort: 18080, Type: "tcp"},
			{PrivatePort: 5432, PublicPort: 15432, Type: "tcp"},
		},
	})
	r.Len(got, 3)

	var httpCfg struct {
		URL string `json:"url"`
	}
	r.NoError(json.Unmarshal(got[1].Config, &httpCfg))
	r.Equal("http://10.0.0.5:18080", httpCfg.URL)

	var tcpCfg struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	r.NoError(json.Unmarshal(got[2].Config, &tcpCfg))
	r.Equal("10.0.0.5", tcpCfg.Host)
	r.Equal(15432, tcpCfg.Port)
}

func TestSuggestContainerChecksUnixHostFallsBackToLoopback(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	got := SuggestContainerChecks(ContainerSuggestInput{
		ContainerID: "id-3",
		Name:        "cache",
		DockerHost:  "unix:///var/run/docker.sock",
		Ports:       []ContainerPort{{PrivatePort: 6379, PublicPort: 16379, Type: "tcp"}},
	})
	r.Len(got, 2)

	var tcpCfg struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	r.NoError(json.Unmarshal(got[1].Config, &tcpCfg))
	r.Equal("127.0.0.1", tcpCfg.Host)
	r.Equal(16379, tcpCfg.Port)
}

func TestSuggestContainerChecksExplicitHostAddressWins(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	got := SuggestContainerChecks(ContainerSuggestInput{
		ContainerID: "id-4",
		Name:        "svc",
		DockerHost:  "unix:///var/run/docker.sock",
		HostAddress: "192.168.1.20",
		Ports:       []ContainerPort{{PrivatePort: 443, PublicPort: 8443, Type: "tcp"}},
	})
	r.Len(got, 2)

	var httpsCfg struct {
		URL string `json:"url"`
	}
	r.NoError(json.Unmarshal(got[1].Config, &httpsCfg))
	r.Equal("https://192.168.1.20:8443", httpsCfg.URL)
}

func TestSuggestContainerChecksSlugsDedupedWithinGroup(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Two published ports that both map to HTTP would collide on a port-less slug
	// were it not for the port suffix + dedup; assert all slugs are distinct.
	got := SuggestContainerChecks(ContainerSuggestInput{
		ContainerID: "id-5",
		Name:        "multi",
		DockerHost:  "tcp://10.0.0.9:2375",
		Ports: []ContainerPort{
			{PrivatePort: 80, PublicPort: 18080, Type: "tcp"},
			{PrivatePort: 8080, PublicPort: 18081, Type: "tcp"},
		},
	})
	r.Len(got, 3) // docker + 2 http

	slugs := make(map[string]struct{})
	for _, s := range got {
		_, dup := slugs[s.Slug]
		r.False(dup, "slugs must be distinct within a group: %s", s.Slug)
		slugs[s.Slug] = struct{}{}
	}
}

func TestSuggestContainerChecksGroupLabelFallsBackToID(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	got := SuggestContainerChecks(ContainerSuggestInput{
		ContainerID: "deadbeef",
		Name:        "",
		DockerHost:  "unix:///var/run/docker.sock",
	})
	r.Len(got, 1)
	r.Equal("deadbeef", got[0].GroupLabel, "blank name must fall back to the container ID")
}
