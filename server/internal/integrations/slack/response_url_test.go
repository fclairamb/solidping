package slack

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// roundTripFunc adapts a func into an http.RoundTripper.
type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// stubResponse builds a minimal response for a recording transport.
func stubResponse(req *http.Request, status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}
}

// recordedRequest is one request seen by a recordingTransport.
type recordedRequest struct {
	method string
	url    string
	body   string
}

// recordingTransport records every request and answers with respond.
type recordingTransport struct {
	mu       sync.Mutex
	requests []recordedRequest
	respond  func(req *http.Request) *http.Response
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body string

	if req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}

		body = string(raw)
	}

	rt.mu.Lock()
	rt.requests = append(rt.requests, recordedRequest{method: req.Method, url: req.URL.String(), body: body})
	rt.mu.Unlock()

	if rt.respond != nil {
		return rt.respond(req), nil
	}

	return stubResponse(req, http.StatusOK), nil
}

func (rt *recordingTransport) seen() []recordedRequest {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	return append([]recordedRequest(nil), rt.requests...)
}

func TestValidateResponseURL(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		url   string
		valid bool
	}{
		{name: "commands URL", url: "https://hooks.slack.com/commands/T0/1/x", valid: true},
		{name: "host is case-insensitive", url: "https://HOOKS.slack.com/actions/T0/1/x", valid: true},
		{name: "plain http", url: "http://hooks.slack.com/commands/T0/1/x"},
		{name: "suffix domain", url: "https://hooks.slack.com.evil.example/"},
		{name: "host in path", url: "https://evil.example/hooks.slack.com"},
		{name: "userinfo", url: "https://user:pass@hooks.slack.com/commands/T0/1/x"},
		{name: "explicit port", url: "https://hooks.slack.com:8443/commands/T0/1/x"},
		{name: "IP literal", url: "https://127.0.0.1/commands/T0/1/x"},
		{name: "empty", url: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateResponseURL(tc.url)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrInvalidResponseURL)
			}
		})
	}
}

// TestPostResponseURL_RefusesNonSlackURL is the SSRF regression: a
// caller-supplied response_url pointing anywhere but hooks.slack.com gets no
// request at all.
func TestPostResponseURL_RefusesNonSlackURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var hits atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	err := postResponseURL(t.Context(), server.URL, ephemeral("hello"))
	r.ErrorIs(err, ErrInvalidResponseURL)
	r.Zero(hits.Load(), "a non-Slack response_url must never be called")
}

// TestPostResponseURLWith_PostsToSlack is the positive control: a valid
// hooks.slack.com URL gets exactly one POST carrying the JSON message.
func TestPostResponseURLWith_PostsToSlack(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	transport := &recordingTransport{}
	client := newResponseURLClient(transport)

	const target = "https://hooks.slack.com/commands/T0/1/x"

	r.NoError(postResponseURLWith(t.Context(), client, target, ephemeral("hello")))

	seen := transport.seen()
	r.Len(seen, 1)
	r.Equal(http.MethodPost, seen[0].method)
	r.Equal(target, seen[0].url)

	var msg MessageResponse
	r.NoError(json.Unmarshal([]byte(seen[0].body), &msg))
	r.Equal("hello", msg.Text)
	r.Equal(ResponseTypeEphemeral, msg.ResponseType)
}

// TestPostResponseURLWith_DoesNotFollowRedirect: the allowed host must not be
// able to bounce the POST to an internal address.
func TestPostResponseURLWith_DoesNotFollowRedirect(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	transport := &recordingTransport{respond: func(req *http.Request) *http.Response {
		resp := stubResponse(req, http.StatusFound)
		resp.Header.Set("Location", "http://127.0.0.1/x")

		return resp
	}}

	err := postResponseURLWith(t.Context(), newResponseURLClient(transport),
		"https://hooks.slack.com/commands/T0/1/x", ephemeral("hello"))
	r.ErrorIs(err, ErrUnexpectedStatus)
	r.Len(transport.seen(), 1, "the redirect must not be followed")
}

// TestResponseURLHTTPClient_RefusesRedirects pins the production client's
// redirect policy, not only the one tests build.
func TestResponseURLHTTPClient_RefusesRedirects(t *testing.T) {
	t.Parallel()

	require.NotNil(t, responseURLHTTPClient.CheckRedirect)
	require.ErrorIs(t, responseURLHTTPClient.CheckRedirect(nil, nil), http.ErrUseLastResponse)
}
