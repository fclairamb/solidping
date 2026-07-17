package envcheck

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsSubsequence(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		a    []string
		b    []string
		want bool
	}{
		{"empty is subsequence", nil, []string{"A", "B"}, true},
		{
			"missing segment",
			[]string{"RATE", "LIMITING", "TRUSTED", "PROXIES"},
			[]string{"SERVER", "RATE", "LIMITING", "TRUSTED", "PROXIES"},
			true,
		},
		{"exact", []string{"A", "B"}, []string{"A", "B"}, true},
		{"out of order", []string{"B", "A"}, []string{"A", "B"}, false},
		{"not present", []string{"A", "X"}, []string{"A", "B", "C"}, false},
		{"longer than b", []string{"A", "B", "C"}, []string{"A", "B"}, false},
	}

	for _, tc := range cases {
		r := require.New(t)
		r.Equalf(tc.want, isSubsequence(tc.a, tc.b), "%s", tc.name)
	}
}

func TestLevenshtein(t *testing.T) {
	t.Parallel()

	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"", "abc", 3},
		{"abc", "", 3},
		{"SP_LOG_LEVL", "SP_LOG_LEVEL", 1},
		{"kitten", "sitting", 3},
		{"flaw", "lawn", 2},
	}

	for _, tc := range cases {
		r := require.New(t)
		r.Equalf(tc.want, levenshtein(tc.a, tc.b), "levenshtein(%q,%q)", tc.a, tc.b)
		r.Equalf(tc.want, levenshtein(tc.b, tc.a), "levenshtein(%q,%q) symmetric", tc.b, tc.a)
	}
}

func TestSuggest(t *testing.T) {
	t.Parallel()

	known := []string{
		"SP_DB_TYPE",
		"SP_LOG_LEVEL",
		"SP_SERVER_LISTEN",
		"SP_SERVER_RATE_LIMITING_BURST",
		"SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES",
	}

	cases := []struct {
		name    string
		unknown string
		want    string
	}{
		{
			name:    "missing segment uses subsequence rule",
			unknown: "SP_RATE_LIMITING_TRUSTED_PROXIES",
			want:    "SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES",
		},
		{
			name:    "character typo uses levenshtein rule",
			unknown: "SP_LOG_LEVL",
			want:    "SP_LOG_LEVEL",
		},
		{
			name:    "no close name yields no suggestion",
			unknown: "SP_TOTALLY_MADE_UP",
			want:    "",
		},
	}

	for _, tc := range cases {
		r := require.New(t)
		r.Equalf(tc.want, suggest(tc.unknown, known), "%s", tc.name)
	}
}
