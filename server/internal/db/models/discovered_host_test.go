package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewDiscoveredHost(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	host := NewDiscoveredHost("org-1", "job-1", "192.168.1.10", DiscoverySourceLAN)

	r.NotEmpty(host.UID)
	r.Equal("org-1", host.OrganizationUID)
	r.Equal("job-1", host.JobUID)
	r.Equal("192.168.1.10", host.IP)
	r.Equal(DiscoverySourceLAN, host.Source)
	r.JSONEq("[]", string(host.OpenPorts))
	r.JSONEq("[]", string(host.SuggestedChecks))
	r.False(host.DiscoveredAt.IsZero())
}

func TestNewDiscoveredHostFreeboxSource(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	host := NewDiscoveredHost("org-1", "job-2", "10.0.0.5", DiscoverySourceFreebox)
	r.Equal(DiscoverySourceFreebox, host.Source)
	r.Equal(DiscoverySource("freebox"), host.Source)
}
