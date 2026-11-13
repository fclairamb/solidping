package checkerdef

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSameRedirectOrigin(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		first string
		next  string
		want  bool
	}{
		{"same scheme same host", "https://a.acme.com/x", "https://a.acme.com/y", true},
		{"same scheme host case differs", "https://A.acme.com/", "https://a.ACME.com/", true},
		{"same scheme explicit default port", "https://a.acme.com/", "https://a.acme.com:443/", true},
		{"same scheme same explicit port", "http://a.acme.com:8080/", "http://a.acme.com:8080/next", true},
		{"upgrade port-less", "http://a.acme.com/", "https://a.acme.com/", true},
		{"upgrade explicit port", "http://a.acme.com:8443/", "https://a.acme.com:8443/", true},
		{"downgrade port-less", "https://a.acme.com/", "http://a.acme.com/", false},
		{"downgrade explicit port", "https://a.acme.com:8443/", "http://a.acme.com:8443/", false},
		{"downgrade default ports", "https://a.acme.com:443/", "http://a.acme.com:80/", false},
		{"cross host", "https://a.acme.com/", "https://b.acme.com/", false},
		{"other port", "https://a.acme.com/", "https://a.acme.com:8443/", false},
		{"other explicit port", "http://a.acme.com:8080/", "http://a.acme.com:8081/", false},
		{"unknown scheme change", "https://a.acme.com/", "ftp://a.acme.com/", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			first, err := url.Parse(tc.first)
			require.NoError(t, err)

			next, err := url.Parse(tc.next)
			require.NoError(t, err)

			require.Equal(t, tc.want, SameRedirectOrigin(first, next))
		})
	}
}
