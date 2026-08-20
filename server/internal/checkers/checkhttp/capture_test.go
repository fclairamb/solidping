package checkhttp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// captureConfig is the minimal opted-in HTTP config used across these tests.
func captureConfig(url string) *HTTPConfig {
	return &HTTPConfig{URL: url, CaptureFailureResponse: true}
}

// runCheck executes the HTTP checker against cfg and returns the result.
func runCheck(t *testing.T, cfg *HTTPConfig) *checkerdef.Result {
	t.Helper()

	checker := &HTTPChecker{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

	defer cancel()

	result, err := checker.Execute(ctx, cfg)
	require.NoError(t, err)
	require.NotNil(t, result)

	return result
}

// newCaptureTestServer starts a server serving a 200 on /ok and a 503 on any
// other path. Started per subtest: a server started in a parent test would be
// closed by its deferred Close before parallel subtests ever ran.
func newCaptureTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("all good at acme"))

			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("<html><body>acme origin unreachable</body></html>"))
	}))
	t.Cleanup(server.Close)

	return server
}

func TestCaptureOnlyWhenFailingAndOptedIn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		path        string
		optIn       bool
		wantStatus  checkerdef.Status
		wantCapture bool
	}{
		{
			name:        "failure with capture enabled",
			path:        "/down",
			optIn:       true,
			wantStatus:  checkerdef.StatusDown,
			wantCapture: true,
		},
		{
			name:       "failure with capture disabled",
			path:       "/down",
			wantStatus: checkerdef.StatusDown,
		},
		{
			name:       "success with capture enabled",
			path:       "/ok",
			optIn:      true,
			wantStatus: checkerdef.StatusUp,
		},
		{
			name:       "success with capture disabled",
			path:       "/ok",
			wantStatus: checkerdef.StatusUp,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			server := newCaptureTestServer(t)
			cfg := &HTTPConfig{URL: server.URL + tt.path, CaptureFailureResponse: tt.optIn}

			result := runCheck(t, cfg)
			r.Equal(tt.wantStatus, result.Status)

			if !tt.wantCapture {
				r.Nil(result.Diagnostics)

				return
			}

			r.NotNil(result.Diagnostics)
			r.NotNil(result.Diagnostics.FailureResponse)

			capture := result.Diagnostics.FailureResponse
			// Positive control: the capture is genuinely populated, so the
			// absence assertions elsewhere in this file mean something.
			r.Contains(capture.Body, "acme origin unreachable")
			r.Equal(http.StatusServiceUnavailable, capture.StatusCode)
			r.Contains(capture.StatusLine, "503")
			r.False(capture.Truncated)
			r.False(capture.Binary)
			r.False(capture.CapturedAt.IsZero())
			r.NotEmpty(capture.RemoteAddr)
			r.Equal(cfg.URL, capture.URL)
			// Region is filled server-side, never by the checker.
			r.Empty(capture.Region)
		})
	}
}

// TestCaptureNeverOnPreResponseFailure pins the "no response existed" rule:
// a timeout, a DNS failure and a TLS failure all return before any response,
// so the capture must be absent even though the check opted in.
func TestCaptureNeverOnPreResponseFailure(t *testing.T) {
	t.Parallel()

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(slow.Close)

	t.Run("timeout", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		checker := &HTTPChecker{}
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)

		defer cancel()

		result, err := checker.Execute(ctx, captureConfig(slow.URL))
		r.NoError(err)
		r.NotNil(result)
		r.NotEqual(checkerdef.StatusUp, result.Status)
		r.Nil(result.Diagnostics, "a timed-out probe never saw a response, so there is nothing to capture")
	})

	t.Run("dns failure", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		result := runCheck(t, captureConfig("http://this-host-does-not-exist.acme.invalid/"))
		r.NotEqual(checkerdef.StatusUp, result.Status)
		r.Nil(result.Diagnostics)
	})

	t.Run("tls failure", func(t *testing.T) {
		t.Parallel()

		tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(tlsServer.Close)

		r := require.New(t)
		// verifySsl defaults to on, and httptest's certificate is self-signed,
		// so the handshake fails before any response exists.
		result := runCheck(t, captureConfig(tlsServer.URL))
		r.NotEqual(checkerdef.StatusUp, result.Status)
		r.Nil(result.Diagnostics)
	})
}

func TestCaptureTruncatesAtCap(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const bodyLen = maxCaptureBodyBytes * 3

	body := strings.Repeat("a", bodyLen)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", "49152")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	result := runCheck(t, captureConfig(server.URL))
	r.NotNil(result.Diagnostics)

	capture := result.Diagnostics.FailureResponse
	r.NotNil(capture)
	r.True(capture.Truncated)
	r.Equal(bodyLen, capture.BodyBytes)
	// The original Content-Length survives, which is what tells a reader how
	// much of the page they are NOT seeing.
	r.EqualValues(bodyLen, capture.ContentLength)
	r.True(strings.HasSuffix(capture.Body, truncationMarker))

	trimmed := strings.TrimSuffix(capture.Body, truncationMarker)
	r.Len(trimmed, maxCaptureBodyBytes)
}

// TestTruncateUTF8KeepsRuneBoundary pins that a cut landing mid-rune backs off
// rather than producing invalid UTF-8 (which JSONB would reject).
func TestTruncateUTF8KeepsRuneBoundary(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	// "é" is two bytes, and the leading ASCII byte shifts the alignment so the
	// cap lands squarely in the MIDDLE of a rune.
	body := []byte("a" + strings.Repeat("é", maxCaptureBodyBytes))
	out, truncated := truncateUTF8(body)
	r.True(truncated)

	trimmed := strings.TrimSuffix(out, truncationMarker)
	// Positive control: the backoff actually had to move — a cut that landed
	// on a boundary would prove nothing.
	r.Equal(maxCaptureBodyBytes-1, len(trimmed))
	r.True(utf8.ValidString(trimmed))
	r.True(json.Valid(mustJSON(t, trimmed)), "truncated body must survive JSON encoding")
}

func mustJSON(t *testing.T, s string) []byte {
	t.Helper()

	raw, err := json.Marshal(s)
	require.NoError(t, err)

	return raw
}

func TestCaptureRedactsSensitiveHeaders(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/plain")
		h.Set("Set-Cookie", "session=super-secret-session-value; Path=/")
		h.Set("Authorization", "Bearer leaked-bearer")
		h.Set("Proxy-Authenticate", "Basic realm=acme")
		h.Set("WWW-Authenticate", "Basic realm=acme")
		h.Set("X-Api-Key", "leaked-api-key")
		h.Set("X-Amz-Security-Token", "leaked-token")
		h.Set("X-Client-Secret", "leaked-secret")
		h.Set("X-Request-Id", "req-42")
		h.Set("Server", "acme-lb")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("bad gateway"))
	}))
	defer server.Close()

	result := runCheck(t, captureConfig(server.URL))
	r.NotNil(result.Diagnostics)

	capture := result.Diagnostics.FailureResponse
	r.NotNil(capture)
	// Positive control: headers were actually captured, so "absent" below is
	// evidence of redaction and not of an empty map.
	r.NotEmpty(capture.Headers)
	r.Equal("req-42", capture.Headers["X-Request-Id"])
	r.Equal("acme-lb", capture.Headers["Server"])

	for _, name := range []string{
		"Set-Cookie", "Authorization", "Proxy-Authenticate", "Www-Authenticate",
		"X-Api-Key", "X-Amz-Security-Token", "X-Client-Secret",
	} {
		r.Equal(redactedMarker, capture.Headers[name], "header %s must be redacted", name)
	}

	// And no secret value survives anywhere in the serialized capture.
	raw, err := json.Marshal(capture)
	r.NoError(err)

	for _, secret := range []string{
		"super-secret-session-value", "leaked-bearer", "leaked-api-key",
		"leaked-token", "leaked-secret",
	} {
		r.NotContains(string(raw), secret)
	}
}

// TestCaptureNeverIncludesRequestHeaders pins that the check's OWN credentials
// (basic auth, secret headers, custom headers) never appear in the capture.
func TestCaptureNeverIncludesRequestHeaders(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer server.Close()

	cfg := captureConfig(server.URL)
	cfg.BasicAuth = "probe:probe-password-secret"
	cfg.SecretHeaders = map[string]string{"X-Probe-Key": "probe-header-secret"}
	cfg.Headers = map[string]string{"X-Probe-Trace": "probe-plain-header"}

	result := runCheck(t, cfg)
	r.NotNil(result.Diagnostics)

	capture := result.Diagnostics.FailureResponse
	r.NotNil(capture)
	r.Equal("boom", capture.Body) // positive control

	raw, err := json.Marshal(capture)
	r.NoError(err)

	for _, secret := range []string{
		"probe-password-secret", "probe-header-secret", "probe-plain-header", "X-Probe-Key",
	} {
		r.NotContains(string(raw), secret)
	}
}

func TestCaptureBinaryGuard(t *testing.T) {
	t.Parallel()

	binaryBody := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0xff, 0xfe, 0xfd}
	invalidUTF8 := []byte("valid prefix \xff\xfe invalid")

	tests := []struct {
		name        string
		contentType string
		body        []byte
	}{
		{name: "binary content type", contentType: "image/png", body: binaryBody},
		{name: "octet stream", contentType: "application/octet-stream", body: []byte("looks textual")},
		{name: "text content type but invalid utf8", contentType: "text/plain", body: invalidUTF8},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write(tt.body)
			}))
			t.Cleanup(server.Close)

			result := runCheck(t, captureConfig(server.URL))
			r.NotNil(result.Diagnostics)

			capture := result.Diagnostics.FailureResponse
			r.NotNil(capture)
			r.True(capture.Binary)
			r.Empty(capture.Body, "a binary/non-UTF-8 body is never stored raw")
			// Metadata only — but real metadata, not an empty shell.
			r.Equal(len(tt.body), capture.BodyBytes)
			r.Contains(capture.ContentType, strings.Split(tt.contentType, ";")[0])

			sum := sha256.Sum256(tt.body)
			r.Equal(hex.EncodeToString(sum[:]), capture.BodySHA256)
		})
	}
}

func TestCaptureTextLikeContentTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		contentType string
		textLike    bool
	}{
		{contentType: "", textLike: true},
		{contentType: "text/plain", textLike: true},
		{contentType: "text/html; charset=utf-8", textLike: true},
		{contentType: "application/json", textLike: true},
		{contentType: "application/problem+json", textLike: true},
		{contentType: "application/vnd.acme.v1+json", textLike: true},
		{contentType: "application/xml", textLike: true},
		{contentType: "application/atom+xml", textLike: true},
		{contentType: "application/octet-stream", textLike: false},
		{contentType: "image/png", textLike: false},
		{contentType: "application/pdf", textLike: false},
	}

	for _, tt := range tests {
		t.Run(tt.contentType, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.textLike, isTextLikeContentType(tt.contentType))
		})
	}
}

// TestCaptureOnBodyPatternFailure covers a failure detected by body matching
// rather than by status code: the capture must still be attached.
func TestCaptureOnBodyPatternFailure(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("acme maintenance in progress"))
	}))
	defer server.Close()

	cfg := captureConfig(server.URL)
	cfg.BodyExpect = "everything is fine"

	result := runCheck(t, cfg)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.NotNil(result.Diagnostics)
	r.NotNil(result.Diagnostics.FailureResponse)
	r.Equal("acme maintenance in progress", result.Diagnostics.FailureResponse.Body)
	r.Equal(http.StatusOK, result.Diagnostics.FailureResponse.StatusCode)
}

// TestCaptureNeverInOutputMap is the load-bearing negative: the capture must be
// invisible to the results table, which persists Output as JSONB.
func TestCaptureNeverInOutputMap(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("distinctive-body-marker-acme"))
	}))
	defer server.Close()

	result := runCheck(t, captureConfig(server.URL))

	// Positive control: the capture really is present on the result.
	r.NotNil(result.Diagnostics)
	r.Contains(result.Diagnostics.FailureResponse.Body, "distinctive-body-marker-acme")

	raw, err := json.Marshal(result.Output)
	r.NoError(err)
	r.NotContains(string(raw), "distinctive-body-marker-acme")
	r.NotContains(string(raw), "failureResponse")
	r.NotContains(string(raw), "diagnostics")
}

// TestCaptureFailureResponseConfigRoundTrip pins the config key contract: the
// canonical snake_case key, the accepted camelCase alias, omission at the
// false default, and a type error on a non-boolean.
func TestCaptureFailureResponseConfigRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("canonical snake_case key", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		cfg := &HTTPConfig{}
		r.NoError(cfg.FromMap(map[string]any{
			"url":                      "https://acme.com",
			"capture_failure_response": true,
		}))
		r.True(cfg.CaptureFailureResponse)
		r.Equal(true, cfg.GetConfig()["capture_failure_response"])
	})

	t.Run("camelCase alias accepted", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		cfg := &HTTPConfig{}
		r.NoError(cfg.FromMap(map[string]any{
			"url":                    "https://acme.com",
			"captureFailureResponse": true,
		}))
		r.True(cfg.CaptureFailureResponse)
		// Always re-emitted under the canonical key, never the alias.
		out := cfg.GetConfig()
		r.Equal(true, out["capture_failure_response"])
		r.NotContains(out, "captureFailureResponse")
	})

	t.Run("omitted at the false default", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		cfg := &HTTPConfig{}
		r.NoError(cfg.FromMap(map[string]any{"url": "https://acme.com"}))
		r.False(cfg.CaptureFailureResponse)
		r.NotContains(cfg.GetConfig(), "capture_failure_response")

		r.NoError(cfg.FromMap(map[string]any{
			"url":                      "https://acme.com",
			"capture_failure_response": false,
		}))
		r.False(cfg.CaptureFailureResponse)
		r.NotContains(cfg.GetConfig(), "capture_failure_response")
	})

	t.Run("non-boolean rejected", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		cfg := &HTTPConfig{}
		err := cfg.FromMap(map[string]any{
			"url":                      "https://acme.com",
			"capture_failure_response": "yes",
		})
		r.Error(err)
		r.Contains(err.Error(), "boolean")
	})

	t.Run("full config round-trip through GetConfig/FromMap", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		original := &HTTPConfig{URL: "https://acme.com", CaptureFailureResponse: true}
		reparsed := &HTTPConfig{}
		r.NoError(reparsed.FromMap(original.GetConfig()))
		r.True(reparsed.CaptureFailureResponse)
	})
}
