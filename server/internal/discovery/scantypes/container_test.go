package scantypes_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/discovery/scantypes"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

func TestContainerBuildJob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		params    string
		wantType  string
		wantCode  string
		wantHosts []string
	}{
		{
			name:      "single unix socket",
			params:    `{"hosts":["unix:///var/run/docker.sock"]}`,
			wantType:  string(jobdef.JobTypeContainerDiscovery),
			wantHosts: []string{"unix:///var/run/docker.sock"},
		},
		{
			name:      "tcp endpoint with timeout",
			params:    `{"hosts":["tcp://10.0.0.5:2375"],"timeout":"15s"}`,
			wantType:  string(jobdef.JobTypeContainerDiscovery),
			wantHosts: []string{"tcp://10.0.0.5:2375"},
		},
		{
			name:      "multiple endpoints, blanks trimmed",
			params:    `{"hosts":["unix:///var/run/docker.sock","  ","tcp://host:2375"]}`,
			wantType:  string(jobdef.JobTypeContainerDiscovery),
			wantHosts: []string{"unix:///var/run/docker.sock", "tcp://host:2375"},
		},
		{
			name:     "missing hosts",
			params:   `{}`,
			wantCode: scantypes.CodeInvalidParameters,
		},
		{
			name:     "empty hosts list",
			params:   `{"hosts":[]}`,
			wantCode: scantypes.CodeInvalidParameters,
		},
		{
			name:     "only blank hosts",
			params:   `{"hosts":["   "]}`,
			wantCode: scantypes.CodeInvalidParameters,
		},
		{
			name:     "bad scheme http",
			params:   `{"hosts":["http://10.0.0.5:2375"]}`,
			wantCode: scantypes.CodeInvalidParameters,
		},
		{
			name:     "bad scheme tls 2376 ssh",
			params:   `{"hosts":["ssh://host"]}`,
			wantCode: scantypes.CodeInvalidParameters,
		},
		{
			name:     "malformed json",
			params:   `{nope`,
			wantCode: scantypes.CodeInvalidParameters,
		},
	}

	def := &scantypes.ContainerDefinition{}
	r0 := require.New(t)
	r0.Equal("container", def.Type())
	r0.Equal(models.DiscoverySourceContainer, def.Source())

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			jobType, cfg, err := def.BuildJob(t.Context(), scantypes.Deps{}, "org-1", json.RawMessage(tc.params))

			if tc.wantCode != "" {
				r.Error(err)
				r.Equal(tc.wantCode, codeOf(err))

				return
			}

			r.NoError(err)
			r.Equal(tc.wantType, jobType)
			r.NotEmpty(cfg)

			var got struct {
				Hosts   []string `json:"hosts"`
				Timeout string   `json:"timeout"`
			}
			r.NoError(json.Unmarshal(cfg, &got))
			r.Equal(tc.wantHosts, got.Hosts)
		})
	}
}
