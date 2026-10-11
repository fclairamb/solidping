package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// TestOrgNotificationRoutes drives the flat, org-level notification endpoints
// through the REAL router (server.go), not the handler package's own harness:
// an unknown notification answers 404 (never 500 or 200), and the list
// refuses to run without a connectionUid. The handler logic itself is covered
// by internal/handlers/incidentnotifications; this pins the route wiring.
func TestOrgNotificationRoutes(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "org-notifications-route-secret"
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

	org := models.NewOrganization("notif-route-org", "Notification Route Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	const password = "notif-route-pass"

	hash, err := passwords.Hash(password)
	r.NoError(err)

	now := time.Now()
	user := models.NewUser("alice@acme.com")
	user.PasswordHash = &hash
	user.EmailVerifiedAt = &now
	r.NoError(dbSvc.CreateUser(ctx, user))

	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleUser)
	member.JoinedAt = &now
	r.NoError(dbSvc.CreateOrganizationMember(ctx, member))

	payload, err := json.Marshal(map[string]string{"org": org.Slug, "email": user.Email, "password": password})
	r.NoError(err)

	loginReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, ts.URL+"/api/v1/auth/login", bytes.NewReader(payload))
	r.NoError(err)
	loginReq.Header.Set("Content-Type", "application/json")

	loginResp, err := http.DefaultClient.Do(loginReq)
	r.NoError(err)

	defer func() { _ = loginResp.Body.Close() }()

	r.Equal(http.StatusOK, loginResp.StatusCode)

	var login struct {
		AccessToken string `json:"accessToken"`
	}
	r.NoError(json.NewDecoder(loginResp.Body).Decode(&login))

	get := func(path string) (int, string) {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+path, nil)
		r.NoError(reqErr)
		req.Header.Set("Authorization", "Bearer "+login.AccessToken)

		resp, doErr := http.DefaultClient.Do(req)
		r.NoError(doErr)

		defer func() { _ = resp.Body.Close() }()

		var body struct {
			Code string `json:"code"`
		}
		r.NoError(json.NewDecoder(resp.Body).Decode(&body))

		return resp.StatusCode, body.Code
	}

	notifPath := "/api/v1/orgs/" + org.Slug + "/notifications"

	status, code := get(notifPath + "/00000000-0000-0000-0000-000000000000")
	r.Equal(http.StatusNotFound, status, "an unknown notification is a 404")
	r.Equal(string(base.ErrorCodeNotFound), code)

	status, code = get(notifPath)
	r.Equal(http.StatusBadRequest, status, "the org-level list requires connectionUid")
	r.Equal(string(base.ErrorCodeValidationError), code)
}
