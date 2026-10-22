package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The agent's discovery allow-list (spec 2026-10-22-04) is checked at startup:
// enabled needs a list, and every entry is a masked IPv4 prefix outside the
// refused ranges. Valid lists are the positive control.
func TestValidateAgentDiscoveryCIDRs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		enabled bool
		cidrs   []string
		wantErr bool
	}{
		{name: "disabled without a list", enabled: false},
		{name: "enabled with a list", enabled: true, cidrs: []string{"10.0.0.0/24", "192.168.1.0/24"}},
		{name: "enabled with an empty list", enabled: true, wantErr: true},
		{name: "IPv6 entry", enabled: true, cidrs: []string{"fd00::/64"}, wantErr: true},
		{name: "4-in-6 entry", enabled: true, cidrs: []string{"::ffff:10.0.0.0/120"}, wantErr: true},
		{name: "host bits", enabled: true, cidrs: []string{"10.0.0.5/24"}, wantErr: true},
		{name: "loopback", enabled: true, cidrs: []string{"127.0.0.0/8"}, wantErr: true},
		{name: "everything", enabled: true, cidrs: []string{"0.0.0.0/0"}, wantErr: true},
		{name: "link-local", enabled: true, cidrs: []string{"169.254.0.0/24"}, wantErr: true},
		{name: "multicast", enabled: true, cidrs: []string{"224.0.0.0/8"}, wantErr: true},
		{name: "garbage", enabled: true, cidrs: []string{"not-a-cidr"}, wantErr: true},
		{name: "a list without enabled is still checked", cidrs: []string{"127.0.0.1/32"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			cfg := &Config{
				Node: NodeConfig{Role: NodeRoleAgent, Name: "test-node"},
				Agent: AgentConfig{
					ServerURL: "https://example.com", DiscoveryEnabled: tt.enabled, DiscoveryCIDRs: tt.cidrs,
				},
			}

			err := cfg.validateNodeConfig()
			if tt.wantErr {
				r.ErrorIs(err, ErrInvalidDiscoveryCIDRs)

				return
			}

			r.NoError(err)
		})
	}
}

// SP_AGENT_DISCOVERY_ENABLED and SP_AGENT_DISCOVERY_CIDRS bind through the
// manual reader.
func TestAgentDiscoveryEnvBinds(t *testing.T) {
	t.Setenv("SP_AGENT_DISCOVERY_ENABLED", "true")
	t.Setenv("SP_AGENT_DISCOVERY_CIDRS", "10.0.0.0/24, 192.168.1.0/24")

	r := require.New(t)

	cfg := &AgentConfig{}
	applyAgentEnv(cfg)

	r.True(cfg.DiscoveryEnabled)
	r.Equal([]string{"10.0.0.0/24", "192.168.1.0/24"}, cfg.DiscoveryCIDRs)

	recognized := RecognizedEnvVars()
	r.Contains(recognized, "SP_AGENT_DISCOVERY_ENABLED")
	r.Contains(recognized, "SP_AGENT_DISCOVERY_CIDRS")
}
