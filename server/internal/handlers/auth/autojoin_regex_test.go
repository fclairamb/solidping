package auth

import (
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
		{"brand label as a subdomain", `.+@mail\.acme\.com`, false},
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
		{"country variant gmx.de", `.+@gmx\.de`, true},
		{"country variant yahoo.co.uk", `.+@yahoo\.co\.uk`, true},
		{"national provider web.de", `.+@web\.de$`, true},
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
				r.ErrorIs(err, ErrInvalidAutoJoinRegex)
			} else {
				r.NoError(err)
			}
		})
	}
}

func TestIsFreeMailDomain(t *testing.T) {
	t.Parallel()

	cases := []struct {
		domain string
		want   bool
	}{
		// Exact entries
		{"gmail.com", true},
		{"web.de", true},
		{"orange.fr", true},
		{"uol.com.br", true},
		// Brand + single TLD
		{"gmx.de", true},
		{"gmx.net", true},
		{"yahoo.fr", true},
		{"hotmail.it", true},
		{"mail.ru", true},
		// Brand + two-level country suffix
		{"yahoo.co.uk", true},
		{"hotmail.com.br", true},
		{"yahoo.co.jp", true},
		// Normalization
		{"GMX.De", true},
		{"  gmx.de ", true},
		// Not free webmail
		{"acme.com", false},
		{"mail.acme.com", false},
		{"live.team.acme.com", false},
		{"gmx.acme.de", false},
		{"acmegmail.com", false},
		{"gmx", false},
		{"gmx.", false},
		{"", false},
	}

	for _, tc := range cases {
		t.Run(tc.domain, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, isFreeMailDomain(tc.domain))
		})
	}
}
