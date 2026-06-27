package discovery

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSuggestChecksGroupedRows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		ip            string
		hostname      string
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
			name:          "ssh port 22 → tcp",
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
			suggestions := SuggestChecks(tc.ip, tc.hostname, tc.icmpReachable, tc.openPorts)

			if tc.wantTypes == nil {
				r.Empty(suggestions)

				return
			}

			r.Len(suggestions, len(tc.wantTypes))

			for i, wantType := range tc.wantTypes {
				r.Equal(wantType, suggestions[i].Type)
				// Every row in a group shares the same group key (the IP).
				r.Equal(tc.ip, suggestions[i].GroupKey)
				r.NotEmpty(suggestions[i].Name)
				r.NotEmpty(suggestions[i].Slug)
				r.NotEmpty(suggestions[i].Config)
			}
		})
	}
}

func TestSuggestChecksHTTPSAndHTTPDistinctSlugs(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// A host with 80/443 open + ICMP reachable yields HTTP/HTTPS/ICMP rows
	// sharing one group_key, distinct slugs, and correct metadata.
	suggestions := SuggestChecks("192.168.1.5", "router", true, []int{80, 443})
	r.Len(suggestions, 3)

	slugs := make(map[string]struct{})
	for _, s := range suggestions {
		r.Equal("192.168.1.5", s.GroupKey)
		r.Equal("router", s.GroupLabel)
		_, dup := slugs[s.Slug]
		r.False(dup, "slugs must be distinct within a group: %s", s.Slug)
		slugs[s.Slug] = struct{}{}

		// Metadata is denormalized and identical across the group's rows.
		var meta struct {
			OpenPorts     []int `json:"openPorts"`
			ICMPReachable bool  `json:"icmpReachable"`
		}
		r.NoError(json.Unmarshal(s.Metadata, &meta))
		r.Equal([]int{80, 443}, meta.OpenPorts)
		r.True(meta.ICMPReachable)
	}

	// ICMP first, then HTTP, then HTTPS.
	r.Equal("ping", suggestions[0].Type)
	r.Equal("http", suggestions[1].Type)
	r.Equal("http", suggestions[2].Type)
}

func TestSuggestChecksGroupLabelFallsBackToIP(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	suggestions := SuggestChecks("10.0.0.9", "", true, nil)
	r.Len(suggestions, 1)
	r.Equal("10.0.0.9", suggestions[0].GroupLabel, "blank hostname must fall back to the IP")
}

func TestCheckSlugDedupWithinGroup(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	seen := make(map[string]struct{})
	r.Equal("router-http", dedupSlug(seen, "router-http"))
	r.Equal("router-http-2", dedupSlug(seen, "router-http"))
	r.Equal("router-http-3", dedupSlug(seen, "router-http"))
	r.Equal("router-https", dedupSlug(seen, "router-https"))
}

func TestCheckNameAndSlug(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("192.168.1.5 · HTTP", checkName("192.168.1.5", "http"))
	r.Equal("192-168-1-5-http", checkSlug("192.168.1.5", "HTTP", 0))
	r.Equal("192-168-1-5-tcp-22", checkSlug("192.168.1.5", "TCP", 22))
}
