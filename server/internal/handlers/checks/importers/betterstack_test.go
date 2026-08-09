package importers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks/importers"
)

// testToken is the fake Better Stack API token used across these tests. It must
// never appear in an error message, a log line, or a converted check.
const testToken = "bstk_super_secret_token_value"

// betterStackServer is a fake Better Stack API serving the paginated fixtures.
type betterStackServer struct {
	*httptest.Server
	mu           sync.Mutex
	authHeaders  []string
	requestPaths []string
}

// newBetterStackServer starts a fake API. The fixture pagination cursors carry
// a {{BASE}} placeholder that is rewritten to the live test-server URL.
func newBetterStackServer(t *testing.T) *betterStackServer {
	t.Helper()

	srv := &betterStackServer{}

	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/monitors", func(w http.ResponseWriter, r *http.Request) {
		srv.record(r)

		fixture := "betterstack-monitors-page1.json"
		if r.URL.Query().Get("page") == "2" {
			fixture = "betterstack-monitors-page2.json"
		}

		srv.write(t, w, fixture)
	})

	mux.HandleFunc("/api/v2/heartbeats", func(w http.ResponseWriter, r *http.Request) {
		srv.record(r)
		srv.write(t, w, "betterstack-heartbeats-page1.json")
	})

	srv.Server = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func (s *betterStackServer) record(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.authHeaders = append(s.authHeaders, r.Header.Get("Authorization"))
	s.requestPaths = append(s.requestPaths, r.URL.RequestURI())
}

func (s *betterStackServer) write(t *testing.T, w http.ResponseWriter, fixture string) {
	t.Helper()

	body := strings.ReplaceAll(string(readFixture(t, fixture)), "{{BASE}}", s.URL)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func (s *betterStackServer) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.requestPaths...)
}

func (s *betterStackServer) auths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.authHeaders...)
}

// betterStackBody builds the request payload for the converter, always with the
// fake token every test asserts is never leaked.
func betterStackBody(t *testing.T, baseURL string) []byte {
	t.Helper()

	body, err := json.Marshal(map[string]string{"token": testToken, "baseUrl": baseURL})
	require.NoError(t, err)

	return body
}

func TestBetterStackConverterGolden(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newBetterStackServer(t)
	conv := importers.NewBetterStackConverter(importers.BetterStackOptions{BaseURL: srv.URL})
	r.Equal("betterstack", conv.Source())

	result, err := conv.Convert(betterStackBody(t, ""))
	r.NoError(err)

	assertGolden(t, "betterstack", result)
}

func TestBetterStackConverterFollowsPaginationAndFetchesHeartbeats(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newBetterStackServer(t)
	conv := importers.NewBetterStackConverter(importers.BetterStackOptions{BaseURL: srv.URL})

	result, err := conv.Convert(betterStackBody(t, ""))
	r.NoError(err)

	// Page 1, page 2 (via the pagination cursor), then heartbeats.
	r.Equal([]string{"/api/v2/monitors", "/api/v2/monitors?page=2", "/api/v2/heartbeats"}, srv.paths())

	// Every request carried the bearer token, and only as a header.
	for _, auth := range srv.auths() {
		r.Equal("Bearer "+testToken, auth)
	}

	for _, path := range srv.paths() {
		r.NotContains(path, testToken)
	}

	doc := result.Document

	// Page-2 monitors landed, so pagination really was followed.
	gateway := checkBySlug(t, doc, "gateway-ping")
	r.Equal("icmp", gateway.Type)
	r.Equal("gw.example.org", gateway.Config["host"])

	// Heartbeats were fetched from the second collection.
	backup := checkBySlug(t, doc, "nightly-backup")
	r.Equal("heartbeat", backup.Type)
	r.Equal("24h", backup.Period)
	r.NotNil(backup.ConfirmationPeriodSeconds)
	r.Equal(3600, *backup.ConfirmationPeriodSeconds)

	paused := checkBySlug(t, doc, "hourly-sync")
	r.False(paused.Enabled)
	r.Nil(paused.ConfirmationPeriodSeconds)

	r.True(warningMentions(result.Warnings, "repoint every"), "heartbeat URL change must be surfaced")
}

func TestBetterStackConverterMapsMonitorTypes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newBetterStackServer(t)
	conv := importers.NewBetterStackConverter(importers.BetterStackOptions{BaseURL: srv.URL})

	result, err := conv.Convert(betterStackBody(t, ""))
	r.NoError(err)

	doc := result.Document
	r.Equal("betterstack", doc.Organization)

	main := checkBySlug(t, doc, "main-site")
	r.Equal("http", main.Type)
	r.Equal("3m", main.Period)
	r.Equal("GET", main.Config["method"])
	r.Equal("30s", main.Config["timeout"])
	r.Equal(map[string]any{"X-Tenant": "acme"}, main.Config["headers"])
	// Credential-bearing headers land in the encrypted secretHeaders map.
	r.Equal(map[string]any{"Authorization": "Bearer app-token"}, main.Config["secretHeaders"])

	api := checkBySlug(t, doc, "api-expected-codes")
	r.Equal([]any{"200", "201"}, api.Config["expected_status_codes"])
	r.Equal("POST", api.Config["method"])
	r.Equal(`{"probe":true}`, api.Config["body"])
	r.Equal(false, api.Config["verifySsl"], "verify_ssl: false maps to verifySsl: false")
	r.Equal(false, api.Config["followRedirects"], "follow_redirects: false maps to followRedirects: false")
	r.NotContains(main.Config, "verifySsl", "verify_ssl: true is the default and is not emitted")
	r.NotContains(main.Config, "followRedirects", "follow_redirects: true is the default and is not emitted")

	shop := checkBySlug(t, doc, "shop-keyword")
	r.False(shop.Enabled, "paused monitors import disabled")
	r.Equal("Add to cart", shop.Config["body_expect"])

	absence := checkBySlug(t, doc, "no-maintenance-banner")
	r.Equal("Scheduled maintenance", absence.Config["body_reject"])

	pg := checkBySlug(t, doc, "postgres-port")
	r.Equal("tcp", pg.Type)
	r.Equal("db.example.org", pg.Config["host"])
	r.Equal(5432, pg.Config["port"])

	udp := checkBySlug(t, doc, "syslog-udp")
	r.Equal("udp", udp.Type)
	r.Equal(514, udp.Config["port"], "string ports are normalized")

	r.Equal("smtp", checkBySlug(t, doc, "mail-relay").Type)
	r.Equal("pop3", checkBySlug(t, doc, "mailbox-pop").Type)
	r.Equal("imap", checkBySlug(t, doc, "mailbox-imap").Type)
	r.Equal("dns", checkBySlug(t, doc, "apex-dns").Type)

	// Unmappable monitor types are dropped with a warning.
	for i := range doc.Checks {
		r.NotEqual("login-journey", doc.Checks[i].Slug)
	}

	r.True(warningMentions(result.Warnings, `type "playwright"`))
	r.True(warningMentions(result.Warnings, "basic-auth credentials were deliberately not imported"))
	r.True(warningMentions(result.Warnings, "monitor groups are not imported"))
	r.True(warningMentions(result.Warnings, "certificate expiry"))
	r.True(warningMentions(result.Warnings, "domain expiry"))
}

func TestBetterStackConverterNeverLeaksTheToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newBetterStackServer(t)
	conv := importers.NewBetterStackConverter(importers.BetterStackOptions{BaseURL: srv.URL})

	result, err := conv.Convert(betterStackBody(t, ""))
	r.NoError(err)

	// The token appears nowhere in the converted document or its warnings.
	encoded, err := json.Marshal(result.Document)
	r.NoError(err)
	r.NotContains(string(encoded), testToken)

	encodedWarnings, err := json.Marshal(result.Warnings)
	r.NoError(err)
	r.NotContains(string(encodedWarnings), testToken)
}

func TestBetterStackConverterUnauthorizedIsCleanAndTokenFree(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// The fake API echoes the credential back in its error body — a real
	// hazard, and the reason we never surface upstream bodies.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errors":"invalid token ` + req.Header.Get("Authorization") + `"}`))
	}))
	t.Cleanup(srv.Close)

	conv := importers.NewBetterStackConverter(importers.BetterStackOptions{BaseURL: srv.URL})

	_, err := conv.Convert(betterStackBody(t, ""))
	r.Error(err)
	r.ErrorIs(err, importers.ErrBetterStackUnauthorized)
	r.NotContains(err.Error(), testToken)
}

func TestBetterStackConverterNetworkFailureIsCleanAndTokenFree(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// A server that is started and immediately closed gives a connection refused.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	baseURL := srv.URL
	srv.Close()

	conv := importers.NewBetterStackConverter(importers.BetterStackOptions{BaseURL: baseURL})

	_, err := conv.Convert(betterStackBody(t, ""))
	r.Error(err)
	r.ErrorIs(err, importers.ErrBetterStackUnreachable)
	r.NotContains(err.Error(), testToken)
}

func TestBetterStackConverterServerErrorDoesNotEchoBody(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`boom ` + req.Header.Get("Authorization")))
	}))
	t.Cleanup(srv.Close)

	conv := importers.NewBetterStackConverter(importers.BetterStackOptions{BaseURL: srv.URL})

	_, err := conv.Convert(betterStackBody(t, ""))
	r.ErrorIs(err, importers.ErrBetterStackAPI)
	r.NotContains(err.Error(), testToken)
	r.NotContains(err.Error(), "boom")
}

func TestBetterStackConverterRejectsCrossHostPagination(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[],"pagination":{"next":"https://evil.example.com/api/v2/monitors?page=2"}}`))
	}))
	t.Cleanup(srv.Close)

	conv := importers.NewBetterStackConverter(importers.BetterStackOptions{BaseURL: srv.URL})

	_, err := conv.Convert(betterStackBody(t, ""))
	r.ErrorIs(err, importers.ErrBetterStackAPI)
	r.Contains(err.Error(), "different host")
}

func TestBetterStackConverterRequiresAToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	conv := importers.NewBetterStackConverter(importers.BetterStackOptions{})

	_, err := conv.Convert([]byte(`{"token":"   "}`))
	r.ErrorIs(err, importers.ErrBetterStackTokenRequired)

	_, err = conv.Convert([]byte(`not json`))
	r.Error(err)
}

func TestBetterStackConverterEmptyAccount(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[],"pagination":{"next":null}}`))
	}))
	t.Cleanup(srv.Close)

	conv := importers.NewBetterStackConverter(importers.BetterStackOptions{BaseURL: srv.URL})

	_, err := conv.Convert(betterStackBody(t, ""))
	r.ErrorIs(err, importers.ErrEmptyInput)
}

func TestBetterStackConverterHonorsRequestBaseURL(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newBetterStackServer(t)

	// Constructed against the production default, redirected by the request body.
	conv := importers.NewBetterStackConverter(importers.BetterStackOptions{})

	result, err := conv.Convert(betterStackBody(t, srv.URL))
	r.NoError(err)
	r.NotEmpty(result.Document.Checks)
}

// TestBetterStackIPVersionMapping covers the one place SolidPing knowingly
// diverges from Better Stack. Their `ip_version` is ipv4 / ipv6 / null, where
// NULL MEANS BOTH FAMILIES; ours pins one family per check. Mapping null onto
// `auto` silently would halve the coverage of every monitor that relied on the
// default, so the importer must say so out loud.
func TestBetterStackIPVersionMapping(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newBetterStackServer(t)
	conv := importers.NewBetterStackConverter(importers.BetterStackOptions{BaseURL: srv.URL})

	result, err := conv.Convert(betterStackBody(t, ""))
	r.NoError(err)

	doc := result.Document

	// An explicitly pinned family is carried across, on the canonical key.
	r.Equal("ipv6", checkBySlug(t, doc, "ipv6-only-tcp").Config["ipVersion"])
	r.Equal("ipv4", checkBySlug(t, doc, "ipv4-pinned-ping").Config["ipVersion"])

	// A monitor that left it unset is imported as auto — no key at all, so the
	// stored config is exactly what an untouched check would carry.
	tcpDefault := checkBySlug(t, doc, "postgres-port")
	_, pinned := tcpDefault.Config["ipVersion"]
	r.False(pinned, "an unset Better Stack ip_version must not write a pinned family")

	// ...but it is reported, naming what the value means on each side and what
	// to do about it. This is the anti-silent-mismap assertion.
	r.True(warningMentions(result.Warnings, "left Better Stack's ip_version unset"))
	r.True(warningMentions(result.Warnings, "monitor over both IPv4 and IPv6"))
	r.True(warningMentions(result.Warnings, "Create a second check with ipVersion: ipv6"))

	// A family pinned on a type SolidPing cannot pin (dns is scoped out) is its
	// own warning rather than a silently dropped setting or an invalid config.
	dnsPinned := checkBySlug(t, doc, "dns-pinned-v6")
	_, dnsHasKey := dnsPinned.Config["ipVersion"]
	r.False(dnsHasKey, "an unsupported pin must not be written into the config")
	r.True(warningMentions(result.Warnings, `SolidPing "dns" checks cannot be pinned`))
}
