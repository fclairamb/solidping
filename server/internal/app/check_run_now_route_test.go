package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// TestCheckRunNowRoute drives POST …/checks/:checkUid/run-now (spec
// 2026-10-04-01) through the real router: a viewer is refused, an editor gets
// the per-region status, and the multi-step run resource is untouched.
func TestCheckRunNowRoute(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "run-now-route-secret"
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

	org := models.NewOrganization("runnow-org", "Run Now Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	const password = "run-now-pass"

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

	check := models.NewCheck(org.UID, "web", "http")
	check.Regions = []string{"eu"}
	r.NoError(dbSvc.CreateCheck(ctx, check))

	base := "/api/v1/orgs/" + org.Slug + "/checks/" + check.UID
	runNowPath := base + "/run-now"

	resp := do(http.MethodPost, runNowPath, viewer, nil)
	_ = resp.Body.Close()
	r.Equal(http.StatusForbidden, resp.StatusCode, "a viewer cannot run a check")

	resp = do(http.MethodPost, runNowPath, "", nil)
	_ = resp.Body.Close()
	r.Equal(http.StatusUnauthorized, resp.StatusCode)

	resp = do(http.MethodPost, runNowPath, editor, nil)

	var accepted struct {
		RequestedAt time.Time `json:"requestedAt"`
		Regions     []struct {
			Region string `json:"region"`
			Status string `json:"status"`
		} `json:"regions"`
	}
	r.Equal(http.StatusOK, resp.StatusCode)
	r.NoError(json.NewDecoder(resp.Body).Decode(&accepted))
	_ = resp.Body.Close()
	r.False(accepted.RequestedAt.IsZero())
	r.Len(accepted.Regions, 1)
	r.Equal("eu", accepted.Regions[0].Region)
	r.Equal("queued", accepted.Regions[0].Status)

	// A run-now request does not create the multi-step run resource.
	resp = do(http.MethodGet, base+"/run", editor, nil)

	var run struct {
		Running bool `json:"running"`
	}
	r.Equal(http.StatusOK, resp.StatusCode)
	r.NoError(json.NewDecoder(resp.Body).Decode(&run))
	_ = resp.Body.Close()
	r.False(run.Running)
}
