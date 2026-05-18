package discovery

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSuggestChecks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		ip            string
		icmpReachable bool
		openPorts     []int
		wantTypes     []string
	}{
		{
			name:          "icmp only",
			ip:            "1.2.3.4",
			icmpReachable: true,
			openPorts:     nil,
			wantTypes:     []string{"ping"},
		},
		{
			name:          "http port 80",
			ip:            "1.2.3.4",
			icmpReachable: false,
			openPorts:     []int{80},
			wantTypes:     []string{"http"},
		},
		{
			name:          "https port 443",
			ip:            "1.2.3.4",
			icmpReachable: false,
			openPorts:     []int{443},
			wantTypes:     []string{"http"},
		},
		{
			name:          "ssh port 22",
			ip:            "1.2.3.4",
			icmpReachable: false,
			openPorts:     []int{22},
			wantTypes:     []string{"tcp"},
		},
		{
			name:          "icmp plus http",
			ip:            "1.2.3.4",
			icmpReachable: true,
			openPorts:     []int{80},
			wantTypes:     []string{"ping", "http"},
		},
		{
			name:          "no response",
			ip:            "1.2.3.4",
			icmpReachable: false,
			openPorts:     nil,
			wantTypes:     nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			suggestions := SuggestChecks(tc.ip, tc.icmpReachable, tc.openPorts)

			if tc.wantTypes == nil {
				r.Empty(suggestions)
				return
			}

			r.Len(suggestions, len(tc.wantTypes))

			for i, wantType := range tc.wantTypes {
				r.Equal(wantType, suggestions[i].Type)
			}
		})
	}
}
