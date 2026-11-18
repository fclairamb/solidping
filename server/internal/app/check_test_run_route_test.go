package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// TestCheckTestRunRoute drives POST …/checks/test-run (spec 2026-11-17-04)
// through the real router: a viewer is refused, an editor gets the steps of an
// unsaved script, and nothing is created.
func TestCheckTestRunRoute(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "test-run-route-secret"
	cfg.Auth.AccessTokenExpiry = time.Hour
	cfg.Auth.RefreshTokenExpiry = 24 * time.Hour
	cfg.FileStorage.Type = "local"
	cfg.FileStorage.LocalRoot = t.TempDir()

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	r.NoError(server.Initialize(ctx))
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	server.SetupRoutes(ctx)

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	dbSvc := server.dbService

	org := models.NewOrganization("testrun-org", "Test Run Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	const password = "test-run-pass"

	hash, err := passwords.Hash(password)
	r.NoError(err)

	now := time.Now()
	addMember := func(email string, role models.MemberRole) {
		user := models.NewUser(email)
		user.PasswordHash = &hash
		user.EmailVerifiedAt = &now
		r.NoError(dbSvc.CreateUser(ctx, user))

		member := models.NewOrganizationMember(org.UID, user.UID, role)
		member.JoinedAt = &now
		r.NoError(dbSvc.CreateOrganizationMember(ctx, member))
	}

	addMember("viewer@acme.com", models.MemberRoleViewer)
	addMember("editor@acme.com", models.MemberRoleUser)

	do := func(method, path, token string, body io.Reader) *http.Response {
		req, reqErr := http.NewRequestWithContext(ctx, method, ts.URL+path, body)
		r.NoError(reqErr)

		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, doErr := http.DefaultClient.Do(req)
		r.NoError(doErr)

		return resp
	}

	login := func(email string) string {
		payload, marshalErr := json.Marshal(map[string]string{"org": org.Slug, "email": email, "password": password})
		r.NoError(marshalErr)

		resp := do(http.MethodPost, "/api/v1/auth/login", "", bytes.NewReader(payload))
		defer func() { _ = resp.Body.Close() }()

		r.Equal(http.StatusOK, resp.StatusCode)

		var out struct {
			AccessToken string `json:"accessToken"`
		}
		r.NoError(json.NewDecoder(resp.Body).Decode(&out))

		return out.AccessToken
	}

	viewer := login("viewer@acme.com")
	editor := login("editor@acme.com")

	path := "/api/v1/orgs/" + org.Slug + "/checks/test-run"
	payload := `{"type":"js","config":{"script":"step('Open', function () {}); ` +
		`step('Click', function () { throw new Error('nope'); }); return { status: 'up' };"}}`

	resp := do(http.MethodPost, path, viewer, strings.NewReader(payload))
	_ = resp.Body.Close()
	r.Equal(http.StatusForbidden, resp.StatusCode, "a viewer cannot test-run")

	resp = do(http.MethodPost, path, "", strings.NewReader(payload))
	_ = resp.Body.Close()
	r.Equal(http.StatusUnauthorized, resp.StatusCode)

	resp = do(http.MethodPost, path, editor, strings.NewReader(`{"type":"http","config":{}}`))
	_ = resp.Body.Close()
	r.Equal(http.StatusBadRequest, resp.StatusCode)

	resp = do(http.MethodPost, path, editor, strings.NewReader(payload))

	var out struct {
		Status  string `json:"status"`
		Message string `json:"message"`
		Steps   []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"steps"`
	}
	r.Equal(http.StatusOK, resp.StatusCode)
	r.NoError(json.NewDecoder(resp.Body).Decode(&out))
	_ = resp.Body.Close()
	r.Equal("error", out.Status)
	r.Equal(`step 2/2 "Click" failed: nope`, out.Message)
	r.Len(out.Steps, 2)
	r.Equal("ok", out.Steps[0].Status)
	r.Equal("failed", out.Steps[1].Status)

	// Per-org rate limit (spec 2026-11-18-01): the editor already spent two
	// runs of the org's budget. Invalid bodies are cheap but still count.
	admitted, limited := 2, false

	for range testRunsPerMinute + 5 {
		resp = do(http.MethodPost, path, editor, strings.NewReader(`{"type":"http","config":{}}`))
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests {
			r.NotEmpty(resp.Header.Get("Retry-After"))

			limited = true

			break
		}

		r.Equal(http.StatusBadRequest, resp.StatusCode)

		admitted++
	}

	r.True(limited, "the per-org limiter must refuse runs past its budget")
	r.GreaterOrEqual(admitted, testRunsPerMinute)
	r.LessOrEqual(admitted, testRunsPerMinute+1, "one token may refill during the loop, no more")

	// The role check still runs first: a viewer gets 403, not 429.
	resp = do(http.MethodPost, path, viewer, strings.NewReader(payload))
	_ = resp.Body.Close()
	r.Equal(http.StatusForbidden, resp.StatusCode, "a viewer cannot test-run, even once the org is limited")

	// Nothing was created.
	checks, _, listErr := dbSvc.ListChecks(ctx, org.UID, nil)
	r.NoError(listErr)
	r.Empty(checks)
}
