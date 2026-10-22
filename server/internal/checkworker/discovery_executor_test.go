package checkworker

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/tasks"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/discoverytask"
)

// The discovery executor exists only on an agent whose operator enabled
// discovery with a valid allow-list (spec 2026-10-22-04).
func TestDiscoveryExecutorNeedsTheOperator(t *testing.T) {
	t.Parallel()

	agent := func(enabled bool, cidrs ...string) *config.Config {
		return &config.Config{
			Node:  config.NodeConfig{Role: config.NodeRoleAgent},
			Agent: config.AgentConfig{DiscoveryEnabled: enabled, DiscoveryCIDRs: cidrs},
		}
	}

	r := require.New(t)
	r.Nil(discoveryExecutor(agent(false, "10.0.0.0/24")), "disabled")
	r.Nil(discoveryExecutor(agent(true)), "no allow-list")
	r.Nil(discoveryExecutor(&config.Config{
		Node:  config.NodeConfig{Role: config.NodeRoleAll},
		Agent: config.AgentConfig{DiscoveryEnabled: true, DiscoveryCIDRs: []string{"10.0.0.0/24"}},
	}), "not an agent")

	executor := discoveryExecutor(agent(true, "10.0.0.0/24"))
	r.NotNil(executor, "positive control")
	r.Equal(discoverytask.Capability, executor.Capability())
}

// A worker built without discovery enabled registers no discovery executor,
// so it reports no "discovery" capability; the crawl executor is the control.
func TestDisabledDiscoveryPublishesNoCapability(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &config.Config{Node: config.NodeConfig{Role: config.NodeRoleAgent}}
	_ = NewAgentCheckWorker(cfg, nil)

	r.NotContains(tasks.Capabilities(), discoverytask.Capability)
	r.Contains(tasks.Capabilities(), "crawl")

	for _, executor := range tasks.Registered() {
		r.NotEqual(discoverytask.Kind, executor.Kind())
	}
}
