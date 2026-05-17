// Package scenario provides full-pipeline end-to-end integration tests that
// exercise the complete check-down → incident → escalation → webhook → recovery
// path against a real Postgres-backed server (via embedded-postgres).
package scenario

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/fclairamb/solidping/server/internal/app"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobworker"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
	"github.com/stretchr/testify/require"
)

// sharedDB holds the single shared embedded-Postgres connection string
// booted by TestMain. Empty string means Postgres is unavailable.
var sharedDB struct {
	dbURL string
}

// Scenario owns a running app.Server, an in-test webhook receiver, and all
// pre-seeded fixtures needed for one scenario run.
type Scenario struct {
	T       *testing.T
	BaseURL string
	OrgSlug string
	Token   string // Personal Access Token for API calls
	UserUID string // UID of the admin user created by seedScenario

	WebhookURL string // URL of the in-test httptest webhook receiver
	Webhooks   *WebhookCollector

	server     *app.Server
	httpServer *httptest.Server
}

// WebhookCollector is a thread-safe, append-only log of every POST body
// received by the in-test webhook httptest.Server. Callers use
// WaitForWebhook to block until a predicate is satisfied or the context
// expires.
type WebhookCollector struct {
	mu       sync.Mutex
	cond     *sync.Cond
	payloads []json.RawMessage
}

// newWebhookCollector creates a new WebhookCollector and an httptest.Server
// that appends every POST body to the collector.
func newWebhookCollector(t *testing.T) (*WebhookCollector, *httptest.Server) {
	t.Helper()

	wc := &WebhookCollector{}
	wc.cond = sync.NewCond(&wc.mu)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		wc.mu.Lock()
		wc.payloads = append(wc.payloads, json.RawMessage(body))
		wc.cond.Broadcast()
		wc.mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))

	t.Cleanup(srv.Close)

	return wc, srv
}

// NewWebhookCollector creates a fresh WebhookCollector and httptest.Server.
// Use this when a test needs more than one independent collector (e.g. multi-
// channel fanout tests).
func NewWebhookCollector(t *testing.T) (*WebhookCollector, *httptest.Server) {
	t.Helper()

	return newWebhookCollector(t)
}

// WaitForWebhook blocks until predicate(payload) returns true for some
// received payload, or until the context is cancelled. Returns the matching
// payload on success, or an error describing the timeout.
func (wc *WebhookCollector) WaitForWebhook(ctx context.Context, predicate func(map[string]any) bool) (map[string]any, error) {
	// Periodically broadcast on cond so we recheck ctx even when no new payloads
	// arrive (avoids spinning, deadlock on cond.Wait when ctx cancels externally).
	done := make(chan struct{})
	defer close(done)

	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				wc.cond.Broadcast()
			}
		}
	}()

	wc.mu.Lock()
	defer wc.mu.Unlock()

	for {
		for _, raw := range wc.payloads {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				continue
			}

			if predicate(m) {
				return m, nil
			}
		}

		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		wc.cond.Wait()
	}
}

// runMigrationsOnce boots a minimal app.Server just to run migrations, then
// closes it. Called from TestMain before tests start to avoid concurrent
// schema-creation races.
func runMigrationsOnce(dbURL string) error {
	const refreshTokenExpiryHours = 24
	cfg := &config.Config{
		Server: config.ServerConfig{
			Listen: ":0",
		},
		Database: config.DatabaseConfig{
			Type: "postgres",
			URL:  dbURL,
		},
		Auth: config.AuthConfig{
			JWTSecret:          "scenario-test-secret",
			AccessTokenExpiry:  1 * time.Hour,
			RefreshTokenExpiry: refreshTokenExpiryHours * time.Hour,
		},
	}

	ctx := context.Background()

	server, err := app.NewServer(ctx, cfg)
	if err != nil {
		return fmt.Errorf("new server: %w", err)
	}

	defer func() { _ = server.Close(ctx) }()

	if err := server.Initialize(ctx); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}

	return nil
}

// NewPostgresScenario boots a full app.Server against the shared embedded-
// Postgres instance initialised by TestMain and returns a ready-to-use
// Scenario. Each scenario creates its own org so tests are isolated and
// parallel-safe.
//
// The job worker is started in a goroutine so that escalation-step and
// notification jobs fire without manual intervention.
//
// The server and worker are shut down automatically via t.Cleanup.
func NewPostgresScenario(t *testing.T) *Scenario {
	t.Helper()

	r := require.New(t)

	if sharedDB.dbURL == "" {
		t.Skip("shared Postgres not initialised — Docker unavailable or TestMain skipped")
	}

	const refreshTokenExpiryHours = 24
	cfg := &config.Config{
		Server: config.ServerConfig{
			Listen: ":0",
			// Use a small job-worker pool: 2 runners per scenario × up to 3
			// parallel scenarios = 6 total. Keep this low to avoid exhausting
			// the embedded-postgres connection limit (50).
			JobWorker: config.JobWorkerConfig{Nb: 2},
		},
		Database: config.DatabaseConfig{
			Type: "postgres",
			URL:  sharedDB.dbURL,
		},
		Auth: config.AuthConfig{
			JWTSecret:          "scenario-test-secret",
			AccessTokenExpiry:  1 * time.Hour,
			RefreshTokenExpiry: refreshTokenExpiryHours * time.Hour,
		},
	}

	ctx := t.Context()

	server, err := app.NewServer(ctx, cfg)
	r.NoError(err, "NewServer")

	// Migrations were already run by TestMain. SetupRoutes only.
	server.SetupRoutes(ctx)

	httpSrv := httptest.NewServer(server.Handler())
	t.Cleanup(httpSrv.Close)

	t.Cleanup(func() {
		_ = server.Close(context.Background())
	})

	// Start the job worker — needed for escalation-step and notification jobs.
	workerCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	worker := jobworker.NewJobWorker(
		server.DBService().DB(),
		server.DBService(),
		cfg,
		server.Services(),
		server.JobSvc(),
	)

	go func() {
		_ = worker.Run(workerCtx)
	}()

	// Per-scenario webhook collector (independent httptest.Server per test).
	webhooks, whSrv := newWebhookCollector(t)

	orgSlug := randOrgSlug()
	userUID, pat := seedScenario(t, server, orgSlug)

	return &Scenario{
		T:          t,
		BaseURL:    httpSrv.URL,
		OrgSlug:    orgSlug,
		Token:      pat,
		UserUID:    userUID,
		WebhookURL: whSrv.URL,
		Webhooks:   webhooks,
		server:     server,
		httpServer: httpSrv,
	}
}

// seedScenario creates the org + admin user + PAT directly in the DB (avoids
// registration email-confirmation flow). Returns the user UID and PAT token.
func seedScenario(t *testing.T, server *app.Server, orgSlug string) (userUID, pat string) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	dbSvc := server.DBService()

	now := time.Now()

	// 1. Create the organization.
	org := &models.Organization{
		UID:       randUID(),
		Slug:      orgSlug,
		Name:      "Scenario Org " + orgSlug,
		CreatedAt: now,
		UpdatedAt: now,
	}
	r.NoError(dbSvc.CreateOrganization(ctx, org), "create org")

	// 2. Create the admin user.
	passwordHash, err := passwords.Hash("scenarioTestPass123")
	r.NoError(err, "hash password")

	user := models.NewUser(orgSlug + "@scenario-test.internal")
	user.Name = "Scenario Admin"
	user.PasswordHash = &passwordHash
	user.CreatedAt = now
	user.UpdatedAt = now

	r.NoError(dbSvc.CreateUser(ctx, user), "create user")
	userUID = user.UID

	// 3. Add the user as admin of the org.
	membership := &models.OrganizationMember{
		UID:             randUID(),
		UserUID:         user.UID,
		OrganizationUID: org.UID,
		Role:            models.MemberRoleAdmin,
		JoinedAt:        &now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	r.NoError(dbSvc.CreateOrganizationMember(ctx, membership), "create membership")

	// 4. Create a PAT scoped to the org. Must start with "pat_" so that the
	//    auth service routes it to ValidatePATToken instead of JWT validation.
	patToken := "pat_scenario_" + randHex(8)

	orgUID := org.UID
	token := &models.UserToken{
		UID:             randUID(),
		UserUID:         user.UID,
		OrganizationUID: &orgUID,
		Token:           patToken,
		Type:            models.TokenTypePAT,
		Properties:      models.JSONMap{"name": "scenario-pat"},
		ExpiresAt:       nil,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	r.NoError(dbSvc.CreateUserToken(ctx, token), "create PAT")

	return userUID, patToken
}

// doJSON issues an HTTP request with an optional JSON body and optional Bearer
// token, and returns the response status code plus the decoded body. Errors
// are fatal to the test.
func doJSON(
	ctx context.Context,
	t *testing.T,
	method, url string,
	bearerToken *string,
	body any,
) (int, map[string]any) {
	t.Helper()

	r := require.New(t)

	var bodyReader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		r.NoError(err)

		bodyReader = newJSONReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	r.NoError(err)

	req.Header.Set("Content-Type", "application/json")

	if bearerToken != nil {
		req.Header.Set("Authorization", "Bearer "+*bearerToken)
	}

	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)

	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	r.NoError(err)

	var out map[string]any
	if len(data) > 0 {
		_ = json.Unmarshal(data, &out)
	}

	return resp.StatusCode, out
}

// randOrgSlug generates a unique org slug safe for use in URL paths.
func randOrgSlug() string {
	return "scn-" + randHex(4)
}

// randUID generates a random UUID-shaped string.
func randUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)

	// Format as UUID v4.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// randHex generates a random hex string of n bytes (output length = 2n).
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)

	return hex.EncodeToString(b)
}

// newJSONReader wraps a JSON byte slice as an io.Reader.
func newJSONReader(raw []byte) io.Reader {
	return &jsonReader{data: raw}
}

type jsonReader struct {
	data []byte
	pos  int
}

func (r *jsonReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}

	n := copy(p, r.data[r.pos:])
	r.pos += n

	return n, nil
}

// ScenarioUserUID returns the UID of the admin user created by seedScenario.
func ScenarioUserUID(t *testing.T, s *Scenario) string {
	t.Helper()

	require.NotEmpty(t, s.UserUID, "scenario user UID must not be empty")

	return s.UserUID
}

