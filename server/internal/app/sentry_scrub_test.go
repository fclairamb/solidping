package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/require"
)

// sentryEventFor builds the event Sentry's own request capture produces for req
// (the same NewRequest SentryMiddleware's hub uses), so the test scrubs the
// real shape rather than a hand-made one.
func sentryEventFor(req *http.Request) *sentry.Event {
	event := sentry.NewEvent()
	event.Request = sentry.NewRequest(req)

	return event
}

func TestScrubSentryEvent(t *testing.T) {
	t.Parallel()

	const (
		handoffCode = "Zm9vYmFyYmF6cXV4LWJhc2U2NHVybC1jb2RlLTQzY2hhcnM"
		accessToken = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1In0.c2ln"
		refresh     = "rt_9f3c1a7e5b2d4c6f"
	)

	cases := []struct {
		name    string
		target  string
		secrets []string
		kept    []string
		// rawBefore are the secrets the SDK's own capture leaves in the query
		// string (it filters a few well-known names, but not `code` or
		// `state`): the gap this hook closes.
		rawBefore []string
	}{
		{
			name:      "handoff code",
			target:    "/d/auth/complete?code=" + handoffCode + "&membershipPending=acme",
			secrets:   []string{handoffCode},
			kept:      []string{"membershipPending=acme"},
			rawBefore: []string{handoffCode},
		},
		{
			name: "legacy token redirect",
			target: "/d/orgs/acme/checks?access_token=" + accessToken +
				"&refresh_token=" + refresh + "&expires_in=3600&org=acme",
			secrets: []string{accessToken, refresh},
			kept:    []string{"expires_in=3600", "org=acme"},
		},
		{
			name:      "case and percent-encoding do not hide a key",
			target:    "/x?Access%5FToken=" + accessToken + "&STATE=nonce",
			secrets:   []string{accessToken, "nonce"},
			rawBefore: []string{"nonce"},
		},
		{
			// Positive control: nothing credential-shaped, nothing touched.
			name:   "ordinary query is left alone",
			target: "/api/v1/orgs/acme/checks?limit=20&q=code+review&status=down",
			kept:   []string{"limit=20", "q=code+review", "status=down"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://solidping.example"+tc.target, nil)
			req.Header.Set("Referer", "https://solidping.example"+tc.target)
			req.Header.Set("Authorization", "Bearer "+accessToken)
			req.Header.Set("Cookie", "access_token="+accessToken)
			req.Header.Set("User-Agent", "test-agent")

			event := sentryEventFor(req)

			// Control: before scrubbing, the event really does carry the
			// secrets — otherwise the assertions below prove nothing.
			for _, secret := range tc.secrets {
				r.Contains(event.Request.Headers["Referer"], secret)
			}

			for _, secret := range tc.rawBefore {
				r.Contains(event.Request.QueryString, secret)
			}

			scrubbed := scrubSentryEvent(event, nil)
			r.NotNil(scrubbed)

			for _, secret := range append([]string{accessToken}, tc.secrets...) {
				r.NotContains(scrubbed.Request.QueryString, secret)
				r.NotContains(scrubbed.Request.URL, secret)

				for name, value := range scrubbed.Request.Headers {
					r.NotContains(value, secret, "header %s", name)
				}
			}

			for _, keep := range tc.kept {
				r.Contains(scrubbed.Request.QueryString, keep)
				r.Contains(scrubbed.Request.Headers["Referer"], keep)
			}

			r.Equal("test-agent", scrubbed.Request.Headers["User-Agent"], "unrelated headers survive")
		})
	}
}

func TestScrubSentryEventWithoutRequest(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	event := sentry.NewEvent()
	event.Message = "no request attached"

	r.Same(event, scrubSentryEvent(event, nil))
	r.Nil(scrubSentryEvent(nil, nil))
}

func TestRedactCredentialURLFragment(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	out := redactCredentialURL("https://solidping.example/d/cb#access_token=abc&token_type=bearer")
	r.NotContains(out, "abc")
	r.Contains(out, "token_type=bearer")
}
