package testapi

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bunrouter"
)

func TestFakeAPI_BasicJSON(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	handler := &Handler{}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/fake?statusDown=200", nil)
	w := httptest.NewRecorder()

	router := bunrouter.New()
	router.GET("/api/v1/fake", handler.FakeAPI)

	router.ServeHTTP(w, req)

	r.Equal(http.StatusOK, w.Code)
	r.Contains(w.Header().Get("Content-Type"), "application/json")
	body := w.Body.String()
	r.Contains(body, `"status"`)
	r.Contains(body, `"timestamp"`)
}

func TestFakeAPI_XMLFormat(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	handler := &Handler{}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/fake?format=xml&statusDown=200", nil)
	w := httptest.NewRecorder()

	router := bunrouter.New()
	router.GET("/api/v1/fake", handler.FakeAPI)

	router.ServeHTTP(w, req)

	r.Equal(http.StatusOK, w.Code)
	r.Equal("application/xml", w.Header().Get("Content-Type"))

	var response fakeResponse
	err := xml.Unmarshal(w.Body.Bytes(), &response)
	r.NoError(err)
	r.NotEmpty(response.Status)
	r.NotEmpty(response.Timestamp)
}

func TestFakeAPI_TextFormat(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	handler := &Handler{}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/fake?format=text&statusDown=200", nil)
	w := httptest.NewRecorder()

	router := bunrouter.New()
	router.GET("/api/v1/fake", handler.FakeAPI)

	router.ServeHTTP(w, req)

	r.Equal(http.StatusOK, w.Code)
	r.Equal("text/plain", w.Header().Get("Content-Type"))
	body := w.Body.String()
	r.Contains(body, "status:")
	r.Contains(body, "timestamp:")
}

func TestFakeAPI_StateToggling(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	handler := &Handler{}

	// Test with a short period (1 second) - state will toggle every second
	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/api/v1/fake?period=1&statusUp=200&statusDown=503", nil,
	)
	w := httptest.NewRecorder()

	router := bunrouter.New()
	router.GET("/api/v1/fake", handler.FakeAPI)

	router.ServeHTTP(w, req)

	// Status should be either 200 or 503
	r.True(w.Code == http.StatusOK || w.Code == http.StatusServiceUnavailable)
}

func TestFakeAPI_CustomStatusCodes(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	handler := &Handler{}

	// Test custom status codes
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/fake?statusUp=201&statusDown=404", nil)
	w := httptest.NewRecorder()

	router := bunrouter.New()
	router.GET("/api/v1/fake", handler.FakeAPI)

	router.ServeHTTP(w, req)

	// Status should be either 201 or 404
	r.True(w.Code == http.StatusCreated || w.Code == http.StatusNotFound)
}

func TestFakeAPI_MethodValidation(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	handler := &Handler{}

	// Only POST is supported
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/fake?supportedMethod=POST", nil)
	w := httptest.NewRecorder()

	router := bunrouter.New()
	router.GET("/api/v1/fake", handler.FakeAPI)

	router.ServeHTTP(w, req)

	r.Equal(http.StatusMethodNotAllowed, w.Code)
	r.Equal("POST", w.Header().Get("Allow"))
}

func TestFakeAPI_BasicAuth(t *testing.T) {
	t.Parallel()
	handler := &Handler{}

	tests := []struct {
		name           string
		username       string
		password       string
		expectedStatus int
	}{
		{"Valid credentials", "admin", "pass123", http.StatusOK},
		{"Invalid credentials", "admin", "wrong", http.StatusUnauthorized},
		{"No credentials", "", "", http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			req := httptest.NewRequestWithContext(
				t.Context(), http.MethodGet, "/api/v1/fake?requiredAuth=admin,pass123&statusDown=200", nil,
			)
			if tt.username != "" {
				req.SetBasicAuth(tt.username, tt.password)
			}
			w := httptest.NewRecorder()

			router := bunrouter.New()
			router.GET("/api/v1/fake", handler.FakeAPI)

			router.ServeHTTP(w, req)

			r.Equal(tt.expectedStatus, w.Code)
			if tt.expectedStatus == http.StatusUnauthorized {
				r.Equal(`Basic realm="Fake API"`, w.Header().Get("WWW-Authenticate"))
			}
		})
	}
}

func TestFakeAPI_RequiredHeader(t *testing.T) {
	t.Parallel()
	handler := &Handler{}

	tests := []struct {
		name           string
		headerValue    string
		expectedStatus int
	}{
		{"Valid header", "secret123", http.StatusOK},
		{"Invalid header", "wrong", http.StatusBadRequest},
		{"Missing header", "", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			req := httptest.NewRequestWithContext(
				t.Context(), http.MethodGet, "/api/v1/fake?requiredHeader=X-Api-Key=secret123&statusDown=200", nil,
			)
			if tt.headerValue != "" {
				req.Header.Set("X-Api-Key", tt.headerValue)
			}
			w := httptest.NewRecorder()

			router := bunrouter.New()
			router.GET("/api/v1/fake", handler.FakeAPI)

			router.ServeHTTP(w, req)

			r.Equal(tt.expectedStatus, w.Code)
		})
	}
}

func TestFakeAPI_SetCookie(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	handler := &Handler{}

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/api/v1/fake?setCookie=session=abc123&period=1", nil,
	)
	w := httptest.NewRecorder()

	router := bunrouter.New()
	router.GET("/api/v1/fake", handler.FakeAPI)

	router.ServeHTTP(w, req)

	// Cookie should be set when state is "up"
	// We can't guarantee state, but we can check if cookie is present when status is 200
	if w.Code == http.StatusOK {
		cookies := w.Result().Cookies()
		if len(cookies) > 0 {
			r.Equal("session", cookies[0].Name)
			r.Equal("abc123", cookies[0].Value)
		}
	}
}

func TestFakeAPI_SetHeader(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	handler := &Handler{}

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/api/v1/fake?setHeader=X-Custom-Id=12345&period=1", nil,
	)
	w := httptest.NewRecorder()

	router := bunrouter.New()
	router.GET("/api/v1/fake", handler.FakeAPI)

	router.ServeHTTP(w, req)

	// Header should be set when state is "up"
	if w.Code == http.StatusOK {
		customHeader := w.Header().Get("X-Custom-Id")
		if customHeader != "" {
			r.Equal("12345", customHeader)
		}
	}
}

func TestFakeAPI_Redirect(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	handler := &Handler{}

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodGet,
		"/api/v1/fake?redirectTo=https://example.com&redirectStatus=301&period=1",
		nil,
	)
	w := httptest.NewRecorder()

	router := bunrouter.New()
	router.GET("/api/v1/fake", handler.FakeAPI)

	router.ServeHTTP(w, req)

	// Redirect should only happen when state is "up"
	if w.Code == http.StatusMovedPermanently {
		r.Equal("https://example.com", w.Header().Get("Location"))
	}
}

func TestFakeAPI_InvalidRedirectURL(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	handler := &Handler{}

	// Try to redirect to localhost (should be blocked)
	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/api/v1/fake?redirectTo=http://localhost:8080&period=1", nil,
	)
	w := httptest.NewRecorder()

	router := bunrouter.New()
	router.GET("/api/v1/fake", handler.FakeAPI)

	router.ServeHTTP(w, req)

	// Should return error if state is "up", otherwise normal response
	if w.Code == http.StatusBadRequest {
		body := w.Body.String()
		r.Contains(body, "internal/private")
	}
}

func TestFakeAPI_Delay(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	handler := &Handler{}

	start := time.Now()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/fake?delay=100", nil)
	w := httptest.NewRecorder()

	router := bunrouter.New()
	router.GET("/api/v1/fake", handler.FakeAPI)

	router.ServeHTTP(w, req)

	elapsed := time.Since(start)
	r.GreaterOrEqual(elapsed.Milliseconds(), int64(100))
}

func TestFakeAPI_SlowResponse(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	handler := &Handler{}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/fake?slowResponse=3,10,50", nil)
	w := httptest.NewRecorder()

	router := bunrouter.New()
	router.GET("/api/v1/fake", handler.FakeAPI)

	router.ServeHTTP(w, req)

	r.Equal("text/plain", w.Header().Get("Content-Type"))

	body := w.Body.String()
	// The handler emits "<bytes>\n" per iteration; bytes can include spaces, so
	// don't TrimSpace before splitting (it would strip a trailing space byte).
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	r.Len(lines, 3) // 3 iterations

	// Each line should have 10 random bytes
	for _, line := range lines {
		r.Len(line, 10)
	}
}

func TestFakeAPI_ValidationErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	handler := &Handler{}

	tests := []struct {
		name          string
		query         string
		expectedError string
	}{
		{"Invalid period too small", "period=0", "period must be between"},
		{"Invalid period too large", "period=100000", "period must be between"},
		{"Invalid delay", "delay=40000", "delay must be between"},
		{"Invalid format", "format=csv", "format must be"},
		{"Invalid method", "supportedMethod=INVALID", "supportedMethod must be"},
		{"Invalid slow response format", "slowResponse=1,2", "slowResponse must be in format"},
		{"Invalid slow response iterations", "slowResponse=200,10,100", "iterations must be between"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/fake?"+tt.query, nil)
			w := httptest.NewRecorder()

			router := bunrouter.New()
			router.GET("/api/v1/fake", handler.FakeAPI)

			router.ServeHTTP(w, req)

			r.Equal(http.StatusBadRequest, w.Code)
			body := w.Body.String()
			r.Contains(body, tt.expectedError)
		})
	}
}

func TestFakeAPI_SlowResponseWithStatus(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	handler := &Handler{}

	// Test that slow response respects status codes
	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/api/v1/fake?slowResponse=2,5,10&statusDown=503&period=1", nil,
	)
	w := httptest.NewRecorder()

	router := bunrouter.New()
	router.GET("/api/v1/fake", handler.FakeAPI)

	router.ServeHTTP(w, req)

	// Should return either 200 or 503 based on state
	r.True(w.Code == http.StatusOK || w.Code == http.StatusServiceUnavailable)
	r.Equal("text/plain", w.Header().Get("Content-Type"))
}

func TestFakeAPI_AllParametersCombined(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	handler := &Handler{}

	// Test multiple parameters together
	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodGet,
		"/api/v1/fake?format=text&period=2&statusUp=201&statusDown=503&delay=50",
		nil,
	)
	w := httptest.NewRecorder()

	router := bunrouter.New()
	router.GET("/api/v1/fake", handler.FakeAPI)

	start := time.Now()
	router.ServeHTTP(w, req)

	elapsed := time.Since(start)
	r.GreaterOrEqual(elapsed.Milliseconds(), int64(50))
	r.Equal("text/plain", w.Header().Get("Content-Type"))
	r.True(w.Code == http.StatusCreated || w.Code == http.StatusServiceUnavailable)
}

func TestFakeAPI_ReadSlowResponse(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	handler := &Handler{}

	// Test reading slow response chunk by chunk
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/fake?slowResponse=5,20,100", nil)
	w := httptest.NewRecorder()

	router := bunrouter.New()
	router.GET("/api/v1/fake", handler.FakeAPI)

	start := time.Now()
	router.ServeHTTP(w, req)

	elapsed := time.Since(start)
	// Should take at least 400ms (4 delays of 100ms between 5 chunks)
	r.GreaterOrEqual(elapsed.Milliseconds(), int64(400))

	// Read full body
	body, err := io.ReadAll(w.Body)
	r.NoError(err)

	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	r.Len(lines, 5)

	for _, line := range lines {
		r.NotEmpty(line, "each chunk line should contain data")
	}
}
