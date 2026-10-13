package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

const ovhDLRPathPrefix = "/api/v1/integrations/ovhsms/dlr/"

// fakeOVH answers /auth/time and records every PUT /sms/sms-acme-1 callBack.
type fakeOVH struct {
	mu        sync.Mutex
	callBacks []string
	calls     int
}

func (f *fakeOVH) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++

	if r.URL.Path == "/auth/time" {
		_, _ = w.Write([]byte(strconv.FormatInt(time.Now().Unix(), 10)))

		return
	}

	if r.Method == http.MethodPut && r.URL.Path == "/sms/sms-acme-1" {
		body, _ := io.ReadAll(r.Body)

		var payload struct {
			CallBack string `json:"callBack"`
		}

		_ = json.Unmarshal(body, &payload)
		f.callBacks = append(f.callBacks, payload.CallBack)
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("null"))
}

func (f *fakeOVH) puts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.callBacks...)
}

func ovhDLRConfig(baseURL, dir, provider string) *config.Config {
	cfg := &config.Config{}
	cfg.Database.Type = "sqlite"
	cfg.Database.Dir = dir
	cfg.Auth.JWTSecret = config.DefaultJWTSecret
	cfg.Auth.AccessTokenExpiry = time.Hour
	cfg.Auth.RefreshTokenExpiry = 24 * time.Hour
	cfg.SMS.Provider = provider
	cfg.SMS.Enabled = true
	cfg.SMS.Sender = "SolidPing"
	cfg.SMS.OVH = config.SMSOVHConfig{
		Endpoint: "ovh-eu", ApplicationKey: "ak", ApplicationSecret: "as", ConsumerKey: "ck",
		ServiceName: "sms-acme-1", BaseURL: baseURL,
	}

	return cfg
}

// bootOVH runs the production startup order and returns the handler.
func bootOVH(t *testing.T, cfg *config.Config) *Server {
	t.Helper()

	ctx := context.Background()

	server, err := NewServer(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	return server
}

func startOVH(t *testing.T, server *Server, cfg *config.Config, fake *fakeOVH, wantPutsBeforeOverlay int) {
	t.Helper()

	ctx := context.Background()

	require.NoError(t, server.Initialize(ctx))
	require.Len(t, fake.puts(), wantPutsBeforeOverlay, "no OVH PUT may happen before InitializeSystemConfig")
	require.NoError(t, server.InitializeSystemConfig(ctx, cfg))
	server.SetupRoutes(ctx)
}

func dlrStatus(t *testing.T, server *Server, token string) int {
	t.Helper()

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, ovhDLRPathPrefix+token+"?id=424242&ptt=4", http.NoBody))

	return rec.Code
}

func TestOVHDeliveryCallback_DerivedTokenAndRestart(t *testing.T) {
	t.Setenv("SP_BASE_URL", "https://status.acme.com")

	fake := &fakeOVH{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	dir := t.TempDir()

	var first string

	for boot := range 2 {
		cfg := ovhDLRConfig(srv.URL, dir, "ovh")
		server := bootOVH(t, cfg)
		startOVH(t, server, cfg, fake, boot)

		puts := fake.puts()
		require.Len(t, puts, boot+1)

		callBack := puts[boot]
		require.True(t, strings.HasPrefix(callBack, "https://status.acme.com"+ovhDLRPathPrefix), callBack)

		token := strings.TrimPrefix(callBack, "https://status.acme.com"+ovhDLRPathPrefix)
		mac := hmac.New(sha256.New, []byte(config.DefaultJWTSecret))
		mac.Write([]byte("solidping:ovhsms:dlr"))
		require.NotEqual(t, hex.EncodeToString(mac.Sum(nil)), token, "token must not derive from the public default")

		// Unknown notification id: the handler still answers 204 for a valid token.
		require.Equal(t, http.StatusNoContent, dlrStatus(t, server, token))
		require.Equal(t, http.StatusNotFound, dlrStatus(t, server, token+"x"))

		if boot == 0 {
			first = callBack
		} else {
			require.Equal(t, first, callBack, "the callBack must be stable across restarts")
		}

		_ = server.dbService.Close()
	}
}

func TestOVHDeliveryCallback_ExplicitToken(t *testing.T) {
	t.Setenv("SP_BASE_URL", "https://status.acme.com")

	fake := &fakeOVH{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	cfg := ovhDLRConfig(srv.URL, t.TempDir(), "ovh")
	cfg.SMS.OVH.DLRToken = "explicit-acme-token"

	server := bootOVH(t, cfg)
	startOVH(t, server, cfg, fake, 0)

	puts := fake.puts()
	require.Len(t, puts, 1)
	require.Equal(t, "https://status.acme.com"+ovhDLRPathPrefix+"explicit-acme-token", puts[0])
	require.Equal(t, http.StatusNoContent, dlrStatus(t, server, "explicit-acme-token"))
}

//nolint:paralleltest // SP_BASE_URL must stay unset, so it runs serially with the Setenv tests.
func TestOVHDeliveryCallback_DefaultBaseURLNotRegistered(t *testing.T) {
	fake := &fakeOVH{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	cfg := ovhDLRConfig(srv.URL, t.TempDir(), "ovh")
	cfg.Server.BaseURL = config.DefaultBaseURL
	cfg.SMS.OVH.DLRToken = "explicit-acme-token"

	server := bootOVH(t, cfg)
	startOVH(t, server, cfg, fake, 0)

	require.Empty(t, fake.puts())
	require.Equal(t, http.StatusNoContent, dlrStatus(t, server, "explicit-acme-token"),
		"receipts must still be accepted when the operator registered the URL by hand")
}

func TestOVHDeliveryCallback_NotOVH(t *testing.T) {
	t.Setenv("SP_BASE_URL", "https://status.acme.com")

	fake := &fakeOVH{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	for _, provider := range []string{"twilio", ""} {
		cfg := ovhDLRConfig(srv.URL, t.TempDir(), provider)
		cfg.SMS.Twilio.AccountSID = "ACacme"
		cfg.SMS.Twilio.AuthToken = "tok"

		server := bootOVH(t, cfg)
		startOVH(t, server, cfg, fake, 0)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Zero(t, fake.calls)
}

func TestOVHBaseURLReachable(t *testing.T) {
	t.Parallel()

	for url, want := range map[string]bool{
		"":                         false,
		config.DefaultBaseURL:      false,
		"http://127.0.0.1:4000":    false,
		"http://localhost:8080":    false,
		"https://status.acme.com":  true,
		"https://status.acme.com/": true,
		"not a url":                false,
	} {
		require.Equal(t, want, ovhBaseURLReachable(url), url)
	}
}
