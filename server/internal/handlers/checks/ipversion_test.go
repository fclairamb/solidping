package checks

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// TestValidateIPVersionConfig covers the write-time gate: an invalid value, the
// option on a type that cannot honor it (notably `dns`, which is scoped out on
// purpose), `ipv6` on `dnsbl`, and the tunnel combination.
func TestValidateIPVersionConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		checkType string
		config    map[string]any
		wantErr   bool
		// wantMessage is a substring the rejection must contain, so the message
		// stays actionable rather than merely present.
		wantMessage string
	}{
		{name: "nil config", checkType: "tcp", config: nil},
		{name: "absent key", checkType: "tcp", config: map[string]any{"host": "example.com"}},
		{name: "auto is always fine", checkType: "tcp", config: map[string]any{"ipVersion": "auto"}},
		{name: "empty is always fine", checkType: "tcp", config: map[string]any{"ipVersion": ""}},
		// auto must stay legal even on a type that cannot pin a family: it is
		// the historical behavior and must never start failing a write.
		{name: "auto on dns", checkType: "dns", config: map[string]any{"ipVersion": "auto"}},
		{name: "auto on postgresql", checkType: "postgresql", config: map[string]any{"ipVersion": "auto"}},

		{name: "ipv4 on tcp", checkType: "tcp", config: map[string]any{"ipVersion": "ipv4"}},
		{name: "ipv6 on tcp", checkType: "tcp", config: map[string]any{"ipVersion": "ipv6"}},
		{name: "ipv6 on http", checkType: "http", config: map[string]any{"ipVersion": "ipv6"}},
		{name: "ipv6 on icmp", checkType: "icmp", config: map[string]any{"ipVersion": "ipv6"}},
		{name: "ipv4 via snake_case", checkType: "tcp", config: map[string]any{"ip_version": "ipv4"}},

		{
			name: "invalid value", checkType: "tcp", config: map[string]any{"ipVersion": "v6"},
			wantErr: true, wantMessage: "auto, ipv4 or ipv6",
		},
		{
			name: "wrong type", checkType: "tcp", config: map[string]any{"ipVersion": 6},
			wantErr: true, wantMessage: "must be a string",
		},
		{
			// The settled decision: DNS is scoped out, and rejects rather than
			// silently ignoring — the option has two incompatible meanings there.
			name: "ipv6 on dns is rejected", checkType: "dns", config: map[string]any{"ipVersion": "ipv6"},
			wantErr: true, wantMessage: `check type "dns" cannot be pinned to an IP version`,
		},
		{
			name: "ipv4 on dns is rejected too", checkType: "dns", config: map[string]any{"ipVersion": "ipv4"},
			wantErr: true, wantMessage: `check type "dns" cannot be pinned`,
		},
		{
			name: "ipv6 on a client-library type is rejected", checkType: "postgresql",
			config: map[string]any{"ipVersion": "ipv6"}, wantErr: true,
			wantMessage: "cannot be pinned to an IP version",
		},
		{
			name: "unknown check type is rejected", checkType: "not-a-type",
			config: map[string]any{"ipVersion": "ipv6"}, wantErr: true,
		},
		{
			name: "ipv4 on dnsbl is fine", checkType: "dnsbl", config: map[string]any{"ipVersion": "ipv4"},
		},
		{
			name: "ipv6 on dnsbl is rejected", checkType: "dnsbl", config: map[string]any{"ipVersion": "ipv6"},
			wantErr: true, wantMessage: "IPv4 blocklists only",
		},
		{
			// The tunnel interaction is handled explicitly rather than left
			// implicit: the bastion resolves and dials, so the family here would
			// be a claim the worker cannot honor.
			name: "pinned family on a tunneled check is rejected", checkType: "tcp",
			config: map[string]any{
				"ipVersion":                        "ipv6",
				checkerdef.TunnelCheckUIDConfigKey: "11111111-1111-1111-1111-111111111111",
			},
			wantErr: true, wantMessage: "tunneled check",
		},
		{
			name: "auto on a tunneled check is fine", checkType: "tcp",
			config: map[string]any{
				"ipVersion":                        "auto",
				checkerdef.TunnelCheckUIDConfigKey: "11111111-1111-1111-1111-111111111111",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			err := validateIPVersionConfig(tt.checkType, tt.config)
			if !tt.wantErr {
				r.NoError(err)

				return
			}

			r.Error(err)

			// Every rejection must be a ConfigError on the ipVersion field, so
			// the dashboard can attach it to the selector.
			configErr := checkerdef.IsConfigError(err)
			r.NotNil(configErr)
			r.Equal(checkerdef.IPVersionConfigKey, configErr.Parameter)

			if tt.wantMessage != "" {
				r.Contains(err.Error(), tt.wantMessage)
			}
		})
	}
}
