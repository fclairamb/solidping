package auth

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateAutoJoinRegex(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		pattern string
		wantErr bool
	}{
		// Allowed
		{"empty disables auto-join", "", false},
		{"strict acme", `[a-z]+@acme\.com`, false},
		{"any local at acme", `.+@acme\.com`, false},
		{"regional acme", `.+@(eu|us)\.acme\.com`, false},
		{"specific subdomain", `.+@team\.acme\.example`, false},
		// Refused — structural
		{"missing @", `.+example\.com`, true},
		{"unparseable", `[broken`, true},
		{"just .*", `.*`, true},
		{"just .+", `.+`, true},
		{"just dot", `.`, true},
		{"any post-at .*", `.+@.*`, true},
		{"any post-at .+", `.+@.+`, true},
		{"any post-at non-at", `.+@[^@]+`, true},
		{"any post-at S", `.+@\S+`, true},
		// Refused — denylist
		{"gmail", `.+@gmail\.com`, true},
		{"outlook", `.+@outlook\.com`, true},
		{"proton", `.+@proton\.me`, true},
		// Refused — probe match catches alternations that include free webmail
		{"alternation includes gmail", `.+@(gmail|acme)\.com`, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			err := validateAutoJoinRegex(tc.pattern)
			if tc.wantErr {
				r.Error(err)
				r.True(
					errors.Is(err, ErrInvalidAutoJoinRegex),
					"expected ErrInvalidAutoJoinRegex, got %v", err,
				)
			} else {
				r.NoError(err)
			}
		})
	}
}
