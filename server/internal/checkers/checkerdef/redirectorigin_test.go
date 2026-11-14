package checkerdef

import (
	"io"
	"net/http"
	"net/url"
	"strings"
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

// TestApplyRedirectHop covers the shared redirect hop rule (spec
// 2026-11-14-02): a cross-origin hop never carries Referer, and one that would
// replay a body (a 307/308) is refused, while a 303 (body dropped) is followed.
func TestApplyRedirectHop(t *testing.T) {
	t.Parallel()

	const (
		origin = "https://a.acme.com/x"
		same   = "https://a.acme.com/y"
		other  = "https://b.acme.com/y"
		get    = http.MethodGet
		post   = http.MethodPost
	)

	cases := []struct {
		name         string
		first        string
		next         string
		method       string
		body         string
		hasBody      bool
		wantCross    bool
		wantWithhold bool
	}{
		{"same origin keeps referer", origin, same, get, "", false, false, false},
		{"same origin 307 with body followed", origin, same, post, "k=v", true, false, false},
		{"cross host", origin, other, get, "", false, true, false},
		{"https to http", origin, "http://a.acme.com/y", get, "", false, true, false},
		{"303 drops the body", origin, other, get, "", false, true, false},
		{"307 with body refused", origin, other, post, "k=v", true, true, true},
		{"307 with empty body followed", origin, other, post, "", true, true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			first, err := url.Parse(tc.first)
			require.NoError(t, err)

			var body io.Reader
			if tc.hasBody {
				body = strings.NewReader(tc.body)
			}

			// What net/http hands CheckRedirect: Referer already set, and on a
			// 307/308 the body re-read through GetBody.
			req, err := http.NewRequestWithContext(t.Context(), tc.method, tc.next, body)
			require.NoError(t, err)
			req.Header.Set("Referer", tc.first+"?api_key=s3cr3t")

			hop := ApplyRedirectHop(first, req)
			require.Equal(t, tc.wantCross, hop.CrossOrigin)
			require.Equal(t, tc.wantWithhold, hop.WithholdBody)

			if tc.wantCross {
				require.Empty(t, req.Header.Get("Referer"))
			} else {
				require.NotEmpty(t, req.Header.Get("Referer"))
			}
		})
	}
}
