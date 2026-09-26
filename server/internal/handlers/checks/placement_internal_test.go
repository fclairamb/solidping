package checks

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// TestRequiredCapabilities pins which capability names a check asks a region
// for. The load-bearing half is the negative cases: a pin that the check's own
// config validation would REJECT never asks a region for that family, because
// placement runs before config validation and would otherwise answer "no cloud
// region can run this check" instead of the field error the caller can act on.
func TestRequiredCapabilities(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		checkType string
		config    map[string]any
		want      []string
	}{
		{
			name:      "a browser check needs a browser",
			checkType: string(checkerdef.CheckTypeBrowser),
			config:    map[string]any{"url": "https://example.com"},
			want:      []string{regions.CapabilityBrowser},
		},
		{
			name:      "a tcp check pinned to ipv6 needs that family",
			checkType: string(checkerdef.CheckTypeTCP),
			config:    map[string]any{"host": "example.com", checkerdef.IPVersionConfigKey: "ipv6"},
			want:      []string{regions.CapabilityIPv6},
		},
		{
			name:      "a tcp check pinned to ipv4 needs that family",
			checkType: string(checkerdef.CheckTypeTCP),
			config:    map[string]any{"host": "example.com", checkerdef.IPVersionConfigKey: "ipv4"},
			want:      []string{regions.CapabilityIPv4},
		},
		{
			name:      "an unpinned check needs nothing",
			checkType: string(checkerdef.CheckTypeTCP),
			config:    map[string]any{"host": "example.com"},
			want:      nil,
		},
		{
			name:      "a browser check pinned to ipv6 asks only for the browser",
			checkType: string(checkerdef.CheckTypeBrowser),
			config: map[string]any{
				"url": "https://example.com", checkerdef.IPVersionConfigKey: "ipv6",
			},
			want: []string{regions.CapabilityBrowser},
		},
		{
			name:      "dns cannot be pinned, so it asks for no family",
			checkType: string(checkerdef.CheckTypeDNS),
			config:    map[string]any{"host": "example.com", checkerdef.IPVersionConfigKey: "ipv6"},
			want:      nil,
		},
		{
			name:      "an unparsable value asks for no family",
			checkType: string(checkerdef.CheckTypeTCP),
			config:    map[string]any{"host": "example.com", checkerdef.IPVersionConfigKey: "v6"},
			want:      nil,
		},
		{
			name:      "a tunneled check cannot be pinned, so it asks for no family",
			checkType: string(checkerdef.CheckTypeTCP),
			config: map[string]any{
				"host": "example.com", checkerdef.IPVersionConfigKey: "ipv6",
				checkerdef.TunnelCheckUIDConfigKey: "some-tunnel-uid",
			},
			want: nil,
		},
		{
			name:      "dnsbl cannot be pinned to ipv6, so it asks for no family",
			checkType: string(checkerdef.CheckTypeDNSBL),
			config:    map[string]any{"host": "example.com", checkerdef.IPVersionConfigKey: "ipv6"},
			want:      nil,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, testCase.want, requiredCapabilities(testCase.checkType, testCase.config))
		})
	}
}
