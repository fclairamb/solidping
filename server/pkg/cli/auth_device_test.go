package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestCLIPATTokenName pins the PAT label the CLI asks the server to mint —
// hostname-derived so the token is identifiable on the dashboard tokens page.
func TestCLIPATTokenName(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	name := cliPATTokenName()
	r.True(strings.HasPrefix(name, "sp CLI on "), "got %q", name)
	r.NotEqual("sp CLI on ", name, "the host part must never be empty")
}

// TestStartDeviceAuthorization checks the CLI speaks the RFC 8628 wire format:
// it sends the client label and reads the snake_case response fields.
func TestStartDeviceAuthorization(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var gotBody atomic.Value

	var gotPath atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		gotPath.Store(req.URL.Path)

		body := map[string]string{}
		_ = json.NewDecoder(req.Body).Decode(&body)
		gotBody.Store(body["clientName"])

		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"device_code":"dc-1","user_code":"WDJP-4KXR",` +
			`"verification_uri":"https://x.test/dash0/device",` +
			`"verification_uri_complete":"https://x.test/dash0/device?user_code=WDJP-4KXR",` +
			`"expires_in":900,"interval":5}`))
	}))
	defer srv.Close()

	authz, err := startDeviceAuthorization(t.Context(), srv.URL, "sp CLI on testhost", false)
	r.NoError(err)
	r.Equal("dc-1", authz.DeviceCode)
	r.Equal("WDJP-4KXR", authz.UserCode)
	r.Equal("https://x.test/dash0/device", authz.VerificationURI)
	r.Equal(900, authz.ExpiresIn)
	r.Equal(5, authz.Interval)
	r.Equal("sp CLI on testhost", gotBody.Load())
	r.Equal(devicePathAuthorize, gotPath.Load())
}

// TestStartDeviceAuthorizationRejectsIncompleteResponse guards against a server
// that answers 200 with nothing usable — the CLI must fail loudly rather than
// poll forever with an empty device code.
func TestStartDeviceAuthorizationRejectsIncompleteResponse(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"expires_in":900}`))
	}))
	defer srv.Close()

	_, err := startDeviceAuthorization(t.Context(), srv.URL, "sp CLI", false)
	r.ErrorIs(err, errDeviceUnexpected)
}

// deviceTokenPoll records what a poll carried, for assertion outside the
// handler goroutine.
type deviceTokenPoll struct {
	path       string
	grantType  string
	deviceCode string
}

// deviceTokenServer replies with a scripted sequence of token-endpoint bodies,
// one per poll, repeating the last one.
func deviceTokenServer(t *testing.T, replies []string) (*httptest.Server, *atomic.Int32, *atomic.Value) {
	t.Helper()

	var polls atomic.Int32

	var seen atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		// Assertions inside a handler must not abort the goroutine (testifylint
		// go-require): record what was seen and let the caller check it.
		body := map[string]string{}
		_ = json.NewDecoder(req.Body).Decode(&body)
		seen.Store(deviceTokenPoll{path: req.URL.Path, grantType: body["grant_type"], deviceCode: body["device_code"]})

		idx := int(polls.Add(1)) - 1
		if idx >= len(replies) {
			idx = len(replies) - 1
		}

		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(replies[idx]))
	}))

	return srv, &polls, &seen
}

// pollTestAuthz builds an authorization response with the fastest polling the
// CLI floor allows, so tests stay quick.
func pollTestAuthz(deviceCode string) *deviceAuthorizationResponse {
	return &deviceAuthorizationResponse{
		DeviceCode: deviceCode,
		UserCode:   "WDJP-4KXR",
		ExpiresIn:  30,
		Interval:   1,
	}
}

// TestPollDeviceTokenOutcomes walks the RFC 8628 §3.5 error vocabulary through
// the CLI polling loop, including that authorization_pending keeps waiting and
// slow_down is absorbed rather than treated as a failure.
func TestPollDeviceTokenOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		replies   []string
		wantToken string
		wantErr   error
		minPolls  int32
	}{
		{
			name:      "approved on the first poll",
			replies:   []string{`{"access_token":"pat_abc","token_type":"Bearer"}`},
			wantToken: "pat_abc",
			minPolls:  1,
		},
		{
			name: "pending then approved",
			replies: []string{
				`{"error":"authorization_pending"}`,
				`{"access_token":"pat_xyz","token_type":"Bearer"}`,
			},
			wantToken: "pat_xyz",
			minPolls:  2,
		},
		{
			name: "slow_down is absorbed, not fatal",
			replies: []string{
				`{"error":"slow_down"}`,
				`{"access_token":"pat_slow","token_type":"Bearer"}`,
			},
			wantToken: "pat_slow",
			minPolls:  2,
		},
		{
			name:     "denied",
			replies:  []string{`{"error":"access_denied"}`},
			wantErr:  errDeviceAccessDenied,
			minPolls: 1,
		},
		{
			name:     "expired",
			replies:  []string{`{"error":"expired_token"}`},
			wantErr:  errDeviceExpired,
			minPolls: 1,
		},
		{
			name:     "unknown error code",
			replies:  []string{`{"error":"invalid_request","error_description":"nope"}`},
			wantErr:  errDeviceUnexpected,
			minPolls: 1,
		},
		{
			name:     "200 with no token",
			replies:  []string{`{"token_type":"Bearer"}`},
			wantErr:  errDeviceNoToken,
			minPolls: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			srv, polls, seen := deviceTokenServer(t, tc.replies)
			defer srv.Close()

			token, err := pollDeviceToken(t.Context(), srv.URL, pollTestAuthz("dc-1"), false)
			if tc.wantErr != nil {
				r.ErrorIs(err, tc.wantErr)
				r.Empty(token)
			} else {
				r.NoError(err)
				r.Equal(tc.wantToken, token)
			}

			r.GreaterOrEqual(polls.Load(), tc.minPolls)

			poll, ok := seen.Load().(deviceTokenPoll)
			r.True(ok, "the CLI must have polled the token endpoint")
			r.Equal(devicePathToken, poll.path)
			r.Equal(deviceGrantType, poll.grantType, "the CLI must send the RFC 8628 grant type")
			r.Equal("dc-1", poll.deviceCode)
		})
	}
}

// TestPollDeviceTokenGivesUpAtExpiry proves the loop is bounded: a server that
// answers authorization_pending forever still terminates once the advertised
// lifetime elapses.
func TestPollDeviceTokenGivesUpAtExpiry(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv, _, _ := deviceTokenServer(t, []string{`{"error":"authorization_pending"}`})
	defer srv.Close()

	authz := pollTestAuthz("dc-forever")
	authz.ExpiresIn = 1

	start := time.Now()
	_, err := pollDeviceToken(t.Context(), srv.URL, authz, false)
	r.ErrorIs(err, errDeviceExpired)
	r.Less(time.Since(start), 10*time.Second, "the loop must not outlive the request")
}

// TestPollDeviceTokenHonorsCancellation makes sure Ctrl-C in the terminal ends
// the flow instead of hanging.
func TestPollDeviceTokenHonorsCancellation(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv, _, _ := deviceTokenServer(t, []string{`{"error":"authorization_pending"}`})
	defer srv.Close()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := pollDeviceToken(ctx, srv.URL, pollTestAuthz("dc-cancel"), false)
	r.Error(err)
}
