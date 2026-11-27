package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

func dockerSocketCheckReq(host string, regions []string) checks.CreateCheckRequest {
	cfg := map[string]any{"containerName": "web"}
	if host != "" {
		cfg["host"] = host
	}

	return checks.CreateCheckRequest{Type: "docker", Config: cfg, Regions: regions}
}

// TestCreateDockerCheck_SocketAllowlist: a docker unix:// host outside
// checkers.docker_socket_paths is refused at save time (spec 2026-11-27-03);
// the default host and a tcp:// host are accepted.
func TestCreateDockerCheck_SocketAllowlist(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org := setupDockerGateService(t, config.DeploymentModeSelfHosted)
	svc.SetDockerSocketAllowlist([]string{"/var/run/docker.sock", "/run/docker.sock"})

	_, err := svc.CreateCheck(ctx, org.Slug, dockerSocketCheckReq("unix:///run/other.sock", nil))
	r.ErrorContains(err, checkerdef.DockerSocketSetting)

	var cfgErr *checkerdef.ConfigError
	r.ErrorAs(err, &cfgErr)
	r.Equal("host", cfgErr.Parameter)

	// Positive controls: the default host, a listed one, a tcp host.
	_, err = svc.CreateCheck(ctx, org.Slug, dockerSocketCheckReq("", nil))
	r.NoError(err)

	_, err = svc.CreateCheck(ctx, org.Slug, dockerSocketCheckReq("unix:///run/docker.sock", nil))
	r.NoError(err)

	_, err = svc.CreateCheck(ctx, org.Slug, dockerSocketCheckReq("tcp://10.0.0.5:2375", nil))
	r.NoError(err)

	// Pinned to private regions only, the agent's own list decides.
	_, err = svc.CreateCheck(ctx, org.Slug, dockerSocketCheckReq("unix:///run/other.sock", []string{"@office"}))
	r.NoError(err)

	// The validate endpoint reports it on the host field.
	resp, err := svc.ValidateCheck(ctx, org.Slug, &checks.ValidateCheckRequest{
		Type: "docker", Config: map[string]any{"host": "unix:///run/other.sock"},
	})
	r.NoError(err)
	r.False(resp.Valid)

	found := false

	for _, field := range resp.Fields {
		if field.Name == "host" {
			r.Contains(field.Message, checkerdef.DockerSocketSetting)

			found = true
		}
	}

	r.True(found, "fields: %+v", resp.Fields)
}

// TestCreateDockerCheck_SocketDefaults: an unwired service applies the default
// list; an empty configured list refuses every unix:// host.
func TestCreateDockerCheck_SocketDefaults(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org := setupDockerGateService(t, config.DeploymentModeSelfHosted)

	_, err := svc.CreateCheck(ctx, org.Slug, dockerSocketCheckReq("unix:///var/run/docker.sock", nil))
	r.NoError(err)

	_, err = svc.CreateCheck(ctx, org.Slug, dockerSocketCheckReq("unix:///run/other.sock", nil))
	r.ErrorContains(err, checkerdef.DockerSocketSetting)

	svc.SetDockerSocketAllowlist([]string{})

	_, err = svc.CreateCheck(ctx, org.Slug, dockerSocketCheckReq("unix:///var/run/docker.sock", nil))
	r.ErrorContains(err, checkerdef.DockerSocketSetting)
}
