package freebox_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/integrations/freebox"
)

// expectedHMAC reproduces the SHA1-HMAC the Freebox session endpoint
// expects, independent of the production helper, so the test really
// catches algorithm drift instead of asserting against itself.
func expectedHMAC(token, challenge string) string {
	mac := hmac.New(sha1.New, []byte(token))
	mac.Write([]byte(challenge))

	return hex.EncodeToString(mac.Sum(nil))
}

func TestSignChallenge(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	got := freebox.SignChallenge("abc123", "challenge")
	r.Equal(expectedHMAC("abc123", "challenge"), got)
	// Spot-check a second pair to guard against accidental constants.
	got2 := freebox.SignChallenge("k", "v")
	r.Equal(expectedHMAC("k", "v"), got2)
	r.NotEqual(got, got2)
}

// fakeFreebox is a tiny in-process mock of the /api/v4/ surface we use.
type fakeFreebox struct {
	t                  *testing.T
	appToken           string
	currentSession     string
	currentChallenge   string
	authorizeStatus    string
	authorizeTrackID   int
	connectionHit      atomic.Int32
	authChallengeCount atomic.Int32
	// sessionAssertErr captures assertion failures that happen on the
	// /login/session/ HTTP-handler goroutine — testify's require.* is
	// banned in handlers because it calls t.FailNow on the wrong
	// goroutine, so we record the error and let the main test goroutine
	// surface it via flush().
	sessionAssertErr atomic.Value // string
}

func (f *fakeFreebox) flush() {
	if v, ok := f.sessionAssertErr.Load().(string); ok && v != "" {
		f.t.Fatal(v)
	}
}

func (f *fakeFreebox) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v4/login/authorize/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			writeOK(f.t, w, freebox.AuthorizeResult{
				AppToken: f.appToken,
				TrackID:  f.authorizeTrackID,
			})
		case http.MethodGet:
			writeOK(f.t, w, freebox.PairingStatus{Status: f.authorizeStatus})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/api/v4/login/", func(w http.ResponseWriter, _ *http.Request) {
		f.authChallengeCount.Add(1)
		writeOK(f.t, w, freebox.LoginChallenge{Challenge: f.currentChallenge})
	})

	mux.HandleFunc("/api/v4/login/session/", func(w http.ResponseWriter, r *http.Request) {
		var body freebox.SessionRequest

		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.sessionAssertErr.Store("decode session body: " + err.Error())
		}

		if !assert.Equal(f.t, freebox.DefaultAppID, body.AppID) {
			f.sessionAssertErr.Store("unexpected app_id: " + body.AppID)
		}

		want := expectedHMAC(f.appToken, f.currentChallenge)
		if !assert.Equal(f.t, want, body.Password) {
			f.sessionAssertErr.Store("unexpected password hash")
		}

		writeOK(f.t, w, freebox.SessionResult{SessionToken: f.currentSession})
	})

	mux.HandleFunc("/api/v4/connection/", func(w http.ResponseWriter, r *http.Request) {
		f.connectionHit.Add(1)

		if r.Header.Get("X-Fbx-App-Auth") != f.currentSession {
			writeErr(f.t, w, "auth_required", "Invalid session token")

			return
		}

		writeOK(f.t, w, map[string]any{"state": "up"})
	})

	return mux
}

func writeOK(t *testing.T, w http.ResponseWriter, result any) {
	t.Helper()

	raw, err := json.Marshal(result)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	out := freebox.APIResponse{Success: true, Result: raw}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(out); err != nil {
		t.Logf("encode envelope: %v", err)
	}
}

func writeErr(t *testing.T, w http.ResponseWriter, code, msg string) {
	t.Helper()

	out := freebox.APIResponse{Success: false, ErrorCode: code, Msg: msg}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(out); err != nil {
		t.Logf("encode envelope: %v", err)
	}
}

func TestClient_AuthorizeReturnsAppToken(t *testing.T) {
	t.Parallel()

	fb := &fakeFreebox{
		t:                t,
		appToken:         "token-from-fb",
		authorizeTrackID: 42,
		authorizeStatus:  freebox.StatusPending,
	}
	srv := httptest.NewServer(fb.handler())
	defer srv.Close()

	r := require.New(t)
	c := freebox.NewClient(srv.URL, "")

	result, err := c.Authorize(context.Background(), "SolidPing-Test")
	r.NoError(err)
	r.Equal("token-from-fb", result.AppToken)
	r.Equal(42, result.TrackID)
}

func TestClient_PollPairingMapsStatus(t *testing.T) {
	t.Parallel()

	for _, status := range []string{
		freebox.StatusPending,
		freebox.StatusGranted,
		freebox.StatusDenied,
		freebox.StatusTimeout,
		freebox.StatusUnknown,
	} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()

			fb := &fakeFreebox{t: t, authorizeStatus: status, appToken: "t"}
			srv := httptest.NewServer(fb.handler())
			defer srv.Close()

			r := require.New(t)
			c := freebox.NewClient(srv.URL, "")

			got, err := c.PollPairing(context.Background(), 7)
			r.NoError(err)
			r.Equal(status, got)
		})
	}
}

func TestClient_GetOpensSessionAndUsesToken(t *testing.T) {
	t.Parallel()

	fb := &fakeFreebox{
		t:                t,
		appToken:         "secret-token",
		currentChallenge: "abc",
		currentSession:   "sess-1",
	}
	srv := httptest.NewServer(fb.handler())
	defer srv.Close()

	r := require.New(t)
	c := freebox.NewClient(srv.URL, "secret-token")

	var got map[string]any
	r.NoError(c.Get(context.Background(), "/api/v4/connection/", &got))
	r.Equal("up", got["state"])
	r.EqualValues(1, fb.authChallengeCount.Load())
	r.EqualValues(1, fb.connectionHit.Load())

	// Second call should reuse the session — no new challenge fetch.
	r.NoError(c.Get(context.Background(), "/api/v4/connection/", &got))
	r.EqualValues(1, fb.authChallengeCount.Load(), "session must be cached across calls")
	r.EqualValues(2, fb.connectionHit.Load())
	fb.flush()
}

func TestClient_GetTransparentSessionRenewalOnAuthRequired(t *testing.T) {
	t.Parallel()

	fb := &fakeFreebox{
		t:                t,
		appToken:         "secret-token",
		currentChallenge: "ch-1",
		currentSession:   "sess-1",
	}
	srv := httptest.NewServer(fb.handler())
	defer srv.Close()

	r := require.New(t)
	c := freebox.NewClient(srv.URL, "secret-token")

	// First call: opens session-1, succeeds.
	r.NoError(c.Get(context.Background(), "/api/v4/connection/", nil))

	// Server-side: rotate the session so the cached one is now stale.
	fb.currentChallenge = "ch-2"
	fb.currentSession = "sess-2"

	// Second call: should hit auth_required with the stale token, drop
	// the cache, fetch a new challenge, and succeed.
	r.NoError(c.Get(context.Background(), "/api/v4/connection/", nil))
	r.EqualValues(2, fb.authChallengeCount.Load(), "second call must fetch a fresh challenge")
	fb.flush()
}

func TestClient_PropagatesAuthRequiredAfterRetry(t *testing.T) {
	t.Parallel()

	// Build a handler that always answers auth_required to
	// /connection/, but happily returns a session — so the retry path
	// gets exhausted and we still surface the error.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/login/", func(w http.ResponseWriter, _ *http.Request) {
		writeOK(t, w, freebox.LoginChallenge{Challenge: "c"})
	})
	mux.HandleFunc("/api/v4/login/session/", func(w http.ResponseWriter, _ *http.Request) {
		writeOK(t, w, freebox.SessionResult{SessionToken: "tok"})
	})

	var hits atomic.Int32

	mux.HandleFunc("/api/v4/connection/", func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)

		writeErr(t, w, "auth_required", "no good")
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	r := require.New(t)
	c := freebox.NewClient(srv.URL, "secret-token")

	err := c.Get(context.Background(), "/api/v4/connection/", nil)
	r.Error(err)
	r.ErrorIs(err, freebox.ErrAuthRequired)
	r.EqualValues(2, hits.Load(), "client should attempt the call exactly twice on persistent auth_required")
}

func TestClient_ServerErrorSurfacesUnexpectedStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := require.New(t)
	c := freebox.NewClient(srv.URL, "")

	_, err := c.Authorize(context.Background(), "x")
	r.Error(err)
	r.ErrorIs(err, freebox.ErrUnexpectedStatus)
}

// Sanity-check that ensureSession refuses to run with an empty token —
// callers should never end up here, but we want the failure mode to be
// clear if they do.
func TestClient_GetWithEmptyTokenFailsCleanly(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		// not reached
	}))
	defer srv.Close()

	r := require.New(t)
	c := freebox.NewClient(srv.URL, "")

	err := c.Get(context.Background(), "/api/v4/connection/", nil)
	r.Error(err)
	r.ErrorIs(err, freebox.ErrAuthRequired)
}

// Defensive smoke: classifyError text contains the API code so logs
// stay searchable.
func TestErrorTextIncludesCode(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeErr(t, w, "ratelimit", "slow down")
	}))
	defer srv.Close()

	r := require.New(t)
	c := freebox.NewClient(srv.URL, "")

	_, err := c.Authorize(context.Background(), "x")
	r.Error(err)
	r.ErrorIs(err, freebox.ErrAPI)
	r.Contains(err.Error(), "ratelimit")
}
