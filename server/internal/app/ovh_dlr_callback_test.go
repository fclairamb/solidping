package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

const ovhDLRPathPrefix = "/api/v1/integrations/ovhsms/dlr/"

// fakeOVH answers GET /auth/time and records the callBack of every
// PUT /sms/sms-acme-1.
type fakeOVH struct {
	mu        sync.Mutex
	callbacks []string
	calls     int
	srv       *httptest.Server
}

func newFakeOVH(t *testing.T) *fakeOVH {
	t.Helper()

	f := &fakeOVH{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		f.calls++

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/auth/time":
			_, _ = w.Write([]byte(`1700000000`))
		case r.Method == http.MethodPut && r.URL.Path == "/sms/sms-acme-1":
			body, _ := io.ReadAll(r.Body)
			var payload map[string]string
			_ = json.Unmarshal(body, &payload)
			f.callbacks = append(f.callbacks, payload["callBack"])
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)

	return f
}

func (f *fakeOVH) puts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.callbacks...)
}

func (f *fakeOVH) totalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls
}

func newOVHDLRConfig(fake *fakeOVH, dir string) *config.Config {
	cfg := &config.Config{}
	cfg.Database.Type = config.DatabaseTypeSQLite
	cfg.Database.Dir = dir
	cfg.Auth.JWTSecret = config.DefaultJWTSecret
	cfg.Auth.AccessTokenExpiry = time.Hour
	cfg.Auth.RefreshTokenExpiry = 24 * time.Hour
	cfg.Server.BaseURL = config.DefaultBaseURL
	cfg.SMS.Enabled = true
	cfg.SMS.Provider = config.SMSProviderOVH
	cfg.SMS.Sender = "acme"
	cfg.SMS.OVH.BaseURL = fake.srv.URL
	cfg.SMS.OVH.ApplicationKey = "ak"
	cfg.SMS.OVH.ApplicationSecret = "as"
	cfg.SMS.OVH.ConsumerKey = "ck"
	cfg.SMS.OVH.ServiceName = "sms-acme-1"

	return cfg
}

// bootOVHServer runs the real startup order and returns the handler.
func bootOVHServer(t *testing.T, cfg *config.Config, fake *fakeOVH) http.Handler {
	t.Helper()

	ctx := context.Background()
	r := require.New(t)

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	before := len(fake.puts())
	r.Len(fake.puts(), before, "no OVH PUT may happen in NewServer")
	r.NoError(server.Initialize(ctx))
	r.Len(fake.puts(), before, "no OVH PUT may happen before InitializeSystemConfig")
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	server.SetupRoutes(ctx)

	return server.Handler()
}

func dlrGet(t *testing.T, h http.Handler, token string) int {
	t.Helper()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		ovhDLRPathPrefix+token+"?id=424242&ptt=4", http.NoBody))

	return rec.Code
}

func TestOVHDLRCallback_DerivedTokenAndRestart(t *testing.T) {
	t.Setenv("SP_BASE_URL", "https://status.acme.com")

	fake := newFakeOVH(t)
	dir := t.TempDir()

	h := bootOVHServer(t, newOVHDLRConfig(fake, dir), fake)

	puts := fake.puts()
	require.Len(t, puts, 1)
	require.True(t, strings.HasPrefix(puts[0], "https://status.acme.com"+ovhDLRPathPrefix), puts[0])

	token := strings.TrimPrefix(puts[0], "https://status.acme.com"+ovhDLRPathPrefix)
	require.NotEmpty(t, token)

	require.NotEqual(t, "ff757c366ba6", token[:12], "token must not derive from the public default")
	require.Equal(t, http.StatusNoContent, dlrGet(t, h, token))
	require.Equal(t, http.StatusNotFound, dlrGet(t, h, token+"x"))

	// Restart on the same database: same callBack.
	bootOVHServer(t, newOVHDLRConfig(fake, dir), fake)

	puts = fake.puts()
	require.Len(t, puts, 2)
	require.Equal(t, puts[0], puts[1])
}

func TestOVHDLRCallback_ExplicitToken(t *testing.T) {
	t.Setenv("SP_BASE_URL", "https://status.acme.com")

	fake := newFakeOVH(t)
	cfg := newOVHDLRConfig(fake, t.TempDir())
	cfg.SMS.OVH.DLRToken = "explicit-acme-token"

	h := bootOVHServer(t, cfg, fake)

	puts := fake.puts()
	require.Len(t, puts, 1)
	require.True(t, strings.HasSuffix(puts[0], "/explicit-acme-token"), puts[0])
	require.Equal(t, http.StatusNoContent, dlrGet(t, h, "explicit-acme-token"))
}

func TestOVHDLRCallback_DefaultBaseURLNotRegistered(t *testing.T) {
	t.Setenv("SP_BASE_URL", "")

	fake := newFakeOVH(t)
	cfg := newOVHDLRConfig(fake, t.TempDir())
	cfg.SMS.OVH.DLRToken = "explicit-acme-token"

	h := bootOVHServer(t, cfg, fake)

	require.Empty(t, fake.puts())
	require.Equal(t, http.StatusNoContent, dlrGet(t, h, "explicit-acme-token"))
}

func TestOVHDLRCallback_NotOVH(t *testing.T) {
	t.Setenv("SP_BASE_URL", "https://status.acme.com")

	fake := newFakeOVH(t)
	cfg := newOVHDLRConfig(fake, t.TempDir())
	cfg.SMS.Provider = config.SMSProviderTwilio
	cfg.SMS.Twilio.AccountSID = "ACacme"
	cfg.SMS.Twilio.AuthToken = "tok"

	bootOVHServer(t, cfg, fake)

	require.Zero(t, fake.totalCalls())
}

func TestPublicBaseURL(t *testing.T) {
	t.Parallel()

	require.False(t, publicBaseURL(""))
	require.False(t, publicBaseURL(config.DefaultBaseURL))
	require.False(t, publicBaseURL("http://127.0.0.1:8080"))
	require.False(t, publicBaseURL("http://localhost:9"))
	require.True(t, publicBaseURL("https://status.acme.com"))
}
