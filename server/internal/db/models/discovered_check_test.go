package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewDiscoveredCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := json.RawMessage(`{"url":"http://192.168.1.10"}`)
	meta := json.RawMessage(`{"openPorts":[80],"icmpReachable":true}`)
	dc := NewDiscoveredCheck(
		"org-1", "job-1", DiscoverySourceLAN,
		"192.168.1.10", "router", "router · HTTP", "192-168-1-10-http", "http",
		cfg, meta,
	)

	r.NotEmpty(dc.UID)
	r.Equal("org-1", dc.OrganizationUID)
	r.Equal("job-1", dc.JobUID)
	r.Equal(DiscoverySourceLAN, dc.Source)
	r.Equal("192.168.1.10", dc.GroupKey)
	r.Equal("router", dc.GroupLabel)
	r.Equal("router · HTTP", dc.Name)
	r.Equal("192-168-1-10-http", dc.Slug)
	r.Equal("http", dc.Type)
	r.JSONEq(`{"url":"http://192.168.1.10"}`, string(dc.Config))
	r.JSONEq(`{"openPorts":[80],"icmpReachable":true}`, string(dc.Metadata))
	r.Nil(dc.PromotedToCheckUID)
	r.False(dc.DiscoveredAt.IsZero())
	r.False(dc.CreatedAt.IsZero())
	r.False(dc.UpdatedAt.IsZero())
}

func TestNewDiscoveredCheckDefaultsConfig(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	dc := NewDiscoveredCheck(
		"org-1", "job-2", DiscoverySourceFreebox,
		"10.0.0.5", "nas", "nas · ICMP", "10-0-0-5-icmp", "icmp",
		nil, nil,
	)
	r.Equal(DiscoverySourceFreebox, dc.Source)
	r.Equal(DiscoverySource("freebox"), dc.Source)
	r.JSONEq("{}", string(dc.Config), "nil config must default to an empty object")
	r.Nil(dc.Metadata)
}
