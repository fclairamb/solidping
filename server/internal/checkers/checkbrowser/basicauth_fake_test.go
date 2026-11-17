package checkbrowser

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/stretchr/testify/require"
)

//nolint:paralleltest // mutates the process-wide settings
func TestBasicAuthEnablesFetchOnceAndAnswersChallenges(t *testing.T) {
	r := require.New(t)
	fake := newFakeBrowser(t)

	var enable map[string]any

	fake.on("Fetch.enable", func(params json.RawMessage) (any, *fakeCDPError) {
		r.NoError(json.Unmarshal(params, &enable))

		return map[string]any{}, nil
	})

	replies := make(chan json.RawMessage, 4)

	fake.on("Fetch.continueWithAuth", func(params json.RawMessage) (any, *fakeCDPError) {
		replies <- params

		return map[string]any{}, nil
	})
	fake.on("Fetch.continueRequest", func(params json.RawMessage) (any, *fakeCDPError) {
		replies <- params

		return map[string]any{}, nil
	})

	session, ctx := openFakeSession(t, fake)

	r.ErrorIs(session.BasicAuth(ctx, "alice", "pw", "ftp://nope"), errBasicAuthOrigin)

	r.NoError(session.BasicAuth(ctx, "alice", "pw", "https://staging.acme.com"))
	r.Equal(true, enable["handleAuthRequests"])

	// Re-arming replaces the credentials without enabling Fetch twice.
	r.NoError(session.BasicAuth(ctx, "bob", "pw2", "https://staging.acme.com/path"))
	r.Equal(1, fake.called("Fetch.enable"))

	pausedRequest := map[string]any{
		"url": "https://staging.acme.com/", "method": "GET", "headers": map[string]any{},
		"initialPriority": "High", "referrerPolicy": "no-referrer",
	}

	fake.emit("Fetch.requestPaused", map[string]any{
		"requestId": "r1", "frameId": "f", "resourceType": "Document",
		"request": pausedRequest,
	})

	select {
	case params := <-replies:
		r.Contains(string(params), `"r1"`)
	case <-time.After(5 * time.Second):
		r.Fail("the paused request was never continued")
	}

	fake.emit("Fetch.authRequired", map[string]any{
		"requestId": "r2", "frameId": "f", "resourceType": "Document",
		"request": pausedRequest,
		"authChallenge": map[string]any{
			"source": "Server", "origin": "https://staging.acme.com", "scheme": "basic", "realm": "x",
		},
	})

	select {
	case params := <-replies:
		r.Contains(string(params), "ProvideCredentials")
		r.Contains(string(params), `"bob"`)
	case <-time.After(5 * time.Second):
		r.Fail("the challenge was never answered")
	}
}

//nolint:paralleltest // mutates the process-wide settings
func TestBasicAuthReportsAFailedEnable(t *testing.T) {
	r := require.New(t)
	fake := newFakeBrowser(t)

	fake.on("Fetch.enable", func(json.RawMessage) (any, *fakeCDPError) {
		return nil, &fakeCDPError{Code: -32000, Message: "Fetch is not available"}
	})

	session, ctx := openFakeSession(t, fake)

	err := session.BasicAuth(ctx, "alice", "secret-pw", "")
	r.ErrorContains(err, "failed to enable authentication handling")
	r.NotContains(err.Error(), "secret-pw")
	r.NotContains(err.Error(), "alice")

	// The listener is registered once even when the enable is retried.
	fake.on("Fetch.enable", func(json.RawMessage) (any, *fakeCDPError) { return map[string]any{}, nil })
	r.NoError(session.BasicAuth(ctx, "alice", "secret-pw", ""))
}

func TestOnFetchEventIgnoresUnrelatedEventsAndAnswersWithoutATarget(t *testing.T) {
	t.Parallel()

	session := armedSession("https://staging.acme.com:443")
	session.browserCtx = t.Context()

	// No page behind the session: replies are dropped, nothing panics.
	session.onFetchEvent(&fetch.EventRequestPaused{RequestID: "1"})
	session.onFetchEvent(authEvent(fetch.AuthChallengeSourceServer, "https://staging.acme.com"))
	session.onFetchEvent(&network.EventRequestWillBeSent{})
}

func TestAnswerChallengeFallsBackToTheRequestURL(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	session := armedSession("https://staging.acme.com:443")

	event := authEvent(fetch.AuthChallengeSourceServer, "")
	event.Request = &network.Request{URL: "https://staging.acme.com/private"}
	r.Equal(fetch.AuthChallengeResponseResponseProvideCredentials, session.answerChallenge(event).Response)

	event = authEvent(fetch.AuthChallengeSourceServer, "")
	event.Request = nil
	r.Equal(fetch.AuthChallengeResponseResponseCancelAuth, session.answerChallenge(event).Response)

	event = authEvent(fetch.AuthChallengeSourceServer, "https://staging.acme.com")
	event.AuthChallenge = nil
	r.Equal(fetch.AuthChallengeResponseResponseCancelAuth, session.answerChallenge(event).Response)
}

func TestResolverRuleBracketsIPv6(t *testing.T) {
	t.Parallel()

	v4 := &hostPin{host: "acme.com", ip: net.ParseIP("203.0.113.9")}
	v6 := &hostPin{host: "acme.com", ip: net.ParseIP("2001:db8::1")}

	require.Equal(t, "MAP acme.com 203.0.113.9", v4.resolverRule())
	require.Equal(t, "MAP acme.com [2001:db8::1]", v6.resolverRule())
}

// TestNavigateReportsRejectedCredentials: a refused challenge lets Chrome
// render the 401 page, so the navigation itself "succeeds" (or fails for an
// unrelated reason); Navigate must still name the origin that refused.
//
//nolint:paralleltest // mutates the process-wide settings
func TestNavigateReportsRejectedCredentials(t *testing.T) {
	r := require.New(t)
	fake := newFakeBrowser(t)

	var session *Session

	fake.on("Page.navigate", func(json.RawMessage) (any, *fakeCDPError) {
		// Chrome keeps asking for credentials until the attempts run out.
		for range maxBasicAuthAttempts + 1 {
			session.answerChallenge(authEvent(fetch.AuthChallengeSourceServer, "http://127.0.0.1:8080"))
		}

		return map[string]any{"frameId": "fake-target", "errorText": "net::ERR_INVALID_AUTH_CREDENTIALS"}, nil
	})

	session, ctx := openFakeSession(t, fake)
	session.auth.set = true
	session.auth.username = "alice"
	session.auth.password = "pw"

	_, err := session.Navigate(ctx, "http://127.0.0.1:8080/private")
	r.ErrorIs(err, ErrBasicAuthRejected)
	r.ErrorContains(err, "http://127.0.0.1:8080")

	// Without credentials in play, the navigation failure is reported as is.
	plain := newFakeBrowser(t)
	plain.on("Page.navigate", func(json.RawMessage) (any, *fakeCDPError) {
		return map[string]any{"frameId": "fake-target", "errorText": "net::ERR_CONNECTION_REFUSED"}, nil
	})

	other, otherCtx := openFakeSession(t, plain)

	_, err = other.Navigate(otherCtx, "http://127.0.0.1:8080/")
	r.ErrorContains(err, "ERR_CONNECTION_REFUSED")
}
