package ovhsms

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSignature pins the OVH signed-request scheme against vectors computed
// independently of the implementation: "$1$" + SHA1 of the six fields joined
// by '+'. A regression here is a hard 403 from OVH that reads like a bad
// credential, so the vectors are spelled out rather than round-tripped.
func TestSignature(t *testing.T) {
	t.Parallel()

	vector := func(secret, ck, method, url, body string, ts int64) string {
		joined := strings.Join([]string{secret, ck, method, url, body, strconv.FormatInt(ts, 10)}, "+")
		sum := sha1.Sum([]byte(joined))

		return "$1$" + hex.EncodeToString(sum[:])
	}

	tests := []struct {
		name      string
		secret    string
		consumer  string
		method    string
		url       string
		body      string
		timestamp int64
	}{
		{
			name: "get with empty body", secret: "s3cr3t", consumer: "ck-1",
			method: http.MethodGet, url: "https://eu.api.ovh.com/1.0/sms/sms-ab-1",
			body: "", timestamp: 1700000000,
		},
		{
			name: "post with json body", secret: "s3cr3t", consumer: "ck-1",
			method: http.MethodPost, url: "https://eu.api.ovh.com/1.0/sms/sms-ab-1/jobs",
			body: `{"message":"hello"}`, timestamp: 1700000042,
		},
		{
			name: "different secret changes the signature", secret: "other", consumer: "ck-1",
			method: http.MethodPost, url: "https://eu.api.ovh.com/1.0/sms/sms-ab-1/jobs",
			body: `{"message":"hello"}`, timestamp: 1700000042,
		},
		{
			name: "different timestamp changes the signature", secret: "s3cr3t", consumer: "ck-1",
			method: http.MethodPost, url: "https://eu.api.ovh.com/1.0/sms/sms-ab-1/jobs",
			body: `{"message":"hello"}`, timestamp: 1700000043,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := Signature(tt.secret, tt.consumer, tt.method, tt.url, tt.body, tt.timestamp)
			require.Equal(t,
				vector(tt.secret, tt.consumer, tt.method, tt.url, tt.body, tt.timestamp), got)
			require.True(t, strings.HasPrefix(got, "$1$"))
		})
	}
}

func TestValidEndpoint(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.True(ValidEndpoint(""))
	r.True(ValidEndpoint(EndpointEU))
	r.True(ValidEndpoint(EndpointCA))
	r.True(ValidEndpoint(EndpointUS))
	r.False(ValidEndpoint("ovh-fr"))
	r.False(ValidEndpoint("https://evil.example.com"))
	// An unvalidated token must never be spliced into a host.
	r.Equal(endpointBaseURLs()[DefaultEndpoint], BaseURLForEndpoint("https://evil.example.com"))
}

// fakeOVH is a minimal stand-in for the OVH API: it serves /auth/time from a
// caller-supplied clock and records the last job request it received.
type fakeOVH struct {
	server *httptest.Server
	// serverNow is the time /auth/time reports.
	serverNow func() time.Time

	lastSignature string
	lastTimestamp string
	lastBody      []byte
	lastPath      string
	lastMethod    string
	jobCalls      atomic.Int32
	timeCalls     atomic.Int32
	// status, when non-zero, is returned instead of a success response.
	status int
}

func newFakeOVH(t *testing.T, serverNow func() time.Time) *fakeOVH {
	t.Helper()

	fake := &fakeOVH{serverNow: serverNow}
	mux := http.NewServeMux()

	mux.HandleFunc("/1.0/auth/time", func(w http.ResponseWriter, _ *http.Request) {
		fake.timeCalls.Add(1)
		_, _ = fmt.Fprintf(w, "%d", fake.serverNow().Unix())
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fake.lastBody = body
		fake.lastPath = r.URL.Path
		fake.lastMethod = r.Method
		fake.lastSignature = r.Header.Get("X-Ovh-Signature")
		fake.lastTimestamp = r.Header.Get("X-Ovh-Timestamp")
		fake.jobCalls.Add(1)

		if fake.status != 0 {
			w.WriteHeader(fake.status)
			_, _ = w.Write([]byte(`{"message":"nope","class":"Client::Forbidden"}`))

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ids":[42],"validReceivers":["+33612345678"],` +
			`"invalidReceivers":[],"totalCreditsRemoved":1,"creditsLeft":99}`))
	})

	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *fakeOVH) baseURL() string { return f.server.URL + "/1.0" }

func testConfig(fake *fakeOVH, localNow func() time.Time) *Config {
	return &Config{
		Endpoint:          EndpointEU,
		BaseURL:           fake.baseURL(),
		ApplicationKey:    "ak",
		ApplicationSecret: "as",
		ConsumerKey:       "ck",
		ServiceName:       "sms-ab12345-1",
		Now:               localNow,
	}
}

// TestSendAlwaysSetsNoStopClause is a negative control on money. Dropping
// noStopClause does not fail a send — OVH happily appends a STOP clause that
// can push the body into a second billed segment — so the regression would be
// silent. Assert the wire body, not the struct.
func TestSendAlwaysSetsNoStopClause(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Unix(1700000000, 0) }
	fake := newFakeOVH(t, now)

	client, err := NewClient(t.Context(), testConfig(fake, now))
	require.NoError(t, err)

	for _, msg := range []string{"short", strings.Repeat("long ", 40), ""} {
		report, sendErr := client.Send(t.Context(), &SendParams{
			Receivers: []string{"+33612345678"},
			Message:   msg,
			Sender:    "SolidPing",
		})
		require.NoError(t, sendErr)
		require.Equal(t, []int64{42}, report.IDs)

		var sent map[string]any
		require.NoError(t, json.Unmarshal(fake.lastBody, &sent))
		require.Equal(t, true, sent["noStopClause"],
			"noStopClause must always be true: without it OVH appends a STOP clause "+
				"that can double the segment cost, and the regression is silent")
		require.Equal(t, "UTF-8", sent["charset"])
		require.Equal(t, "SolidPing", sent["sender"])
		require.Equal(t, "/1.0/sms/sms-ab12345-1/jobs", fake.lastPath)
	}
}

// TestClockSkewDeltaIsApplied proves the /auth/time delta is fetched and used.
// The local clock is deliberately 90 seconds behind OVH's; the signed
// timestamp must be OVH's, not the local one, or every request 403s with what
// looks like a credential error.
func TestClockSkewDeltaIsApplied(t *testing.T) {
	t.Parallel()

	const skew = 90 * time.Second

	localNow := func() time.Time { return time.Unix(1700000000, 0) }
	serverNow := func() time.Time { return time.Unix(1700000000, 0).Add(skew) }

	fake := newFakeOVH(t, serverNow)

	client, err := NewClient(t.Context(), testConfig(fake, localNow))
	require.NoError(t, err)
	require.Positive(t, fake.timeCalls.Load(), "/auth/time must be fetched at construction")

	_, err = client.Send(t.Context(), &SendParams{
		Receivers: []string{"+33612345678"}, Message: "hi", Sender: "SolidPing",
	})
	require.NoError(t, err)

	sentTS, err := strconv.ParseInt(fake.lastTimestamp, 10, 64)
	require.NoError(t, err)
	require.Equal(t, serverNow().Unix(), sentTS,
		"the signed timestamp must be OVH server time, not the local clock")

	// And the signature must be computed over that same corrected timestamp.
	expected := Signature("as", "ck", http.MethodPost,
		fake.baseURL()+"/sms/sms-ab12345-1/jobs", string(fake.lastBody), sentTS)
	require.Equal(t, expected, fake.lastSignature)

	// The delta is cached: a second send does not refetch /auth/time.
	before := fake.timeCalls.Load()
	_, err = client.Send(t.Context(), &SendParams{
		Receivers: []string{"+33612345678"}, Message: "hi again", Sender: "SolidPing",
	})
	require.NoError(t, err)
	require.Equal(t, before, fake.timeCalls.Load(), "the server-time delta must be cached")
}

// TestNoSkewSignsWithLocalClock is the positive control for the test above: a
// synchronized clock must sign with the local time unchanged, so the skew
// correction cannot be a constant that happens to match.
func TestNoSkewSignsWithLocalClock(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Unix(1700000000, 0) }
	fake := newFakeOVH(t, now)

	client, err := NewClient(t.Context(), testConfig(fake, now))
	require.NoError(t, err)

	_, err = client.Send(t.Context(), &SendParams{
		Receivers: []string{"+33612345678"}, Message: "hi", Sender: "SolidPing",
	})
	require.NoError(t, err)
	require.Equal(t, strconv.FormatInt(now().Unix(), 10), fake.lastTimestamp)
}

func TestNewClientRejectsBadInput(t *testing.T) {
	t.Parallel()

	base := func(mutate func(*Config)) *Config {
		cfg := &Config{
			Endpoint: EndpointEU, BaseURL: "http://127.0.0.1:1/1.0",
			ApplicationKey: "ak", ApplicationSecret: "as",
			ConsumerKey: "ck", ServiceName: "sms-1",
		}
		mutate(cfg)

		return cfg
	}

	tests := []struct {
		name   string
		mutate func(*Config)
		errIs  error
	}{
		{"unknown endpoint", func(c *Config) { c.Endpoint = "ovh-fr" }, ErrUnknownEndpoint},
		{"no application key", func(c *Config) { c.ApplicationKey = "" }, ErrMissingCredentials},
		{"no application secret", func(c *Config) { c.ApplicationSecret = "" }, ErrMissingCredentials},
		{"no consumer key", func(c *Config) { c.ConsumerKey = "" }, ErrMissingCredentials},
		{"no service name", func(c *Config) { c.ServiceName = " " }, ErrMissingCredentials},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewClient(t.Context(), base(tt.mutate))
			require.ErrorIs(t, err, tt.errIs)
		})
	}
}

// TestUnreachableAuthTimeIsNotFatal proves a transient /auth/time failure
// degrades to a zero delta and a retry on the next send, instead of taking the
// whole channel down at construction.
func TestUnreachableAuthTimeIsNotFatal(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Unix(1700000000, 0) }

	var timeUp atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("/1.0/auth/time", func(w http.ResponseWriter, _ *http.Request) {
		if !timeUp.Load() {
			w.WriteHeader(http.StatusBadGateway)

			return
		}
		_, _ = fmt.Fprintf(w, "%d", now().Add(30*time.Second).Unix())
	})

	var lastTS atomic.Value

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		lastTS.Store(r.Header.Get("X-Ovh-Timestamp"))
		_, _ = w.Write([]byte(`{"ids":[7],"validReceivers":["+33612345678"]}`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := NewClient(t.Context(), &Config{
		Endpoint: EndpointEU, BaseURL: srv.URL + "/1.0",
		ApplicationKey: "ak", ApplicationSecret: "as",
		ConsumerKey: "ck", ServiceName: "sms-1", Now: now,
	})
	require.NoError(t, err, "an unreachable /auth/time must not fail construction")

	// First send: no delta known, local clock used.
	_, err = client.Send(t.Context(), &SendParams{
		Receivers: []string{"+33612345678"}, Message: "hi", Sender: "SolidPing",
	})
	require.NoError(t, err)
	require.Equal(t, strconv.FormatInt(now().Unix(), 10), lastTS.Load())

	// Once OVH answers again the delta heals on the next send.
	timeUp.Store(true)

	_, err = client.Send(t.Context(), &SendParams{
		Receivers: []string{"+33612345678"}, Message: "hi", Sender: "SolidPing",
	})
	require.NoError(t, err)
	require.Equal(t, strconv.FormatInt(now().Add(30*time.Second).Unix(), 10), lastTS.Load())
}

func TestVerifyCredentials(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Unix(1700000000, 0) }

	t.Run("accepted", func(t *testing.T) {
		t.Parallel()

		fake := newFakeOVH(t, now)
		client, err := NewClient(t.Context(), testConfig(fake, now))
		require.NoError(t, err)
		require.NoError(t, client.VerifyCredentials(t.Context()))
		require.Equal(t, http.MethodGet, fake.lastMethod)
	})

	t.Run("rejected", func(t *testing.T) {
		t.Parallel()

		fake := newFakeOVH(t, now)
		client, err := NewClient(t.Context(), testConfig(fake, now))
		require.NoError(t, err)

		fake.status = http.StatusForbidden
		require.ErrorIs(t, client.VerifyCredentials(t.Context()), ErrCredentialsRejected)
	})

	t.Run("unverifiable", func(t *testing.T) {
		t.Parallel()

		fake := newFakeOVH(t, now)
		client, err := NewClient(t.Context(), testConfig(fake, now))
		require.NoError(t, err)

		fake.status = http.StatusInternalServerError
		require.ErrorIs(t, client.VerifyCredentials(t.Context()), ErrCredentialsUnverifiable)
	})
}

func TestSendRejectsWhenNoReceiverAccepted(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Unix(1700000000, 0) }
	fake := newFakeOVH(t, now)

	mux := http.NewServeMux()
	mux.HandleFunc("/1.0/auth/time", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "%d", now().Unix())
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ids":[],"validReceivers":[],"invalidReceivers":["nope"]}`))
	})

	strict := httptest.NewServer(mux)
	t.Cleanup(strict.Close)

	cfg := testConfig(fake, now)
	cfg.BaseURL = strict.URL + "/1.0"

	client, err := NewClient(t.Context(), cfg)
	require.NoError(t, err)

	_, err = client.Send(t.Context(), &SendParams{
		Receivers: []string{"nope"}, Message: "hi", Sender: "SolidPing",
	})
	require.ErrorIs(t, err, ErrNoReceiverAccepted)
}

func TestSetDeliveryCallback(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Unix(1700000000, 0) }
	fake := newFakeOVH(t, now)

	client, err := NewClient(t.Context(), testConfig(fake, now))
	require.NoError(t, err)

	require.NoError(t, client.SetDeliveryCallback(
		t.Context(), "https://solidping.example/api/v1/integrations/ovhsms/dlr/tok"))

	require.Equal(t, http.MethodPut, fake.lastMethod)
	require.Equal(t, "/1.0/sms/sms-ab12345-1", fake.lastPath)

	var sent map[string]string
	require.NoError(t, json.Unmarshal(fake.lastBody, &sent))
	require.Equal(t,
		"https://solidping.example/api/v1/integrations/ovhsms/dlr/tok", sent["callBack"])
}

func TestPttState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw   string
		state DeliveryState
	}{
		{"4", DeliveryDelivered},
		{"3", DeliveryPending},
		{"1", DeliveryPending},
		{"2", DeliveryPending},
		{"5", DeliveryFailed},
		{"6", DeliveryUnknown},
		{"23", DeliveryFailed},
		{"202", DeliveryFailed},
		{"999", DeliveryUnknown},
		{"", DeliveryUnknown},
		{"<script>", DeliveryUnknown},
	}

	for _, tt := range tests {
		t.Run("ptt="+tt.raw, func(t *testing.T) {
			t.Parallel()

			state, desc := PttState(tt.raw)
			require.Equal(t, tt.state, state)
			require.NotEmpty(t, desc)
			require.NotContains(t, desc, "<script>", "the description must never echo raw input")
		})
	}
}
