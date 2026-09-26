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
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

const checkShotsTestPassword = "check-shots-pass"

// TestCheckScreenshotRoutesAuthorization drives the two check screenshot routes
// (spec 2026-09-25-34) through the REAL router and middleware chain:
//
//   - a viewer reads the listing (same authorization as reading the check) but
//     cannot trigger "Capture now" (a write);
//   - a member of ANOTHER org can read neither;
//   - a user of the org triggers a capture, and the second one inside the
//     minute is rate limited.
func TestCheckScreenshotRoutesAuthorization(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "check-shots-route-secret"
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

	org := models.NewOrganization("shots-org", "Shots Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	other := models.NewOrganization("shots-other", "Other Org")
	r.NoError(dbSvc.CreateOrganization(ctx, other))

	hash, err := passwords.Hash(checkShotsTestPassword)
	r.NoError(err)

	now := time.Now()
	addMember := func(orgUID, email string, role models.MemberRole) {
		user := models.NewUser(email)
		user.PasswordHash = &hash
		user.EmailVerifiedAt = &now
		r.NoError(dbSvc.CreateUser(ctx, user))

		member := models.NewOrganizationMember(orgUID, user.UID, role)
		member.JoinedAt = &now
		r.NoError(dbSvc.CreateOrganizationMember(ctx, member))
	}

	addMember(org.UID, "viewer@acme.com", models.MemberRoleViewer)
	addMember(org.UID, "user@acme.com", models.MemberRoleUser)
	addMember(other.UID, "outsider@acme.com", models.MemberRoleAdmin)

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

	login := func(orgSlug, email string) string {
		payload, marshalErr := json.Marshal(map[string]string{
			"org": orgSlug, "email": email, "password": checkShotsTestPassword,
		})
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

	viewer := login(org.Slug, "viewer@acme.com")
	user := login(org.Slug, "user@acme.com")
	outsider := login(other.Slug, "outsider@acme.com")

	check := models.NewCheck(org.UID, "home", "browser")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	store := attachments.NewService(files.NewService(dbSvc, cfg), dbSvc, cfg)
	_, err = store.PutCheckScreenshot(ctx, org.UID, check.UID,
		[]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'x'},
		models.JSONMap{attachments.DetailKeyCheckUID: check.UID})
	r.NoError(err)

	listPath := "/api/v1/orgs/" + org.Slug + "/checks/" + check.UID + "/screenshots"
	capturePath := listPath + "/capture"

	status := func(method, path, token string) int {
		resp := do(method, path, token, nil)
		defer func() { _ = resp.Body.Close() }()

		return resp.StatusCode
	}

	resp := do(http.MethodGet, listPath, viewer, nil)
	r.Equal(http.StatusOK, resp.StatusCode)

	var listed struct {
		Data []attachments.CheckScreenshot `json:"data"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&listed))
	_ = resp.Body.Close()
	r.Len(listed.Data, 1, "a viewer reads the check's captures")

	r.Equal(http.StatusForbidden, status(http.MethodPost, capturePath, viewer), "a viewer cannot trigger a capture")
	r.Equal(http.StatusUnauthorized, status(http.MethodGet, listPath, ""), "anonymous callers are refused")

	outsiderStatus := status(http.MethodGet, listPath, outsider)
	r.Contains([]int{http.StatusForbidden, http.StatusNotFound}, outsiderStatus,
		"another org's member cannot read the captures")
	r.Contains([]int{http.StatusForbidden, http.StatusNotFound}, status(http.MethodPost, capturePath, outsider))

	r.Equal(http.StatusAccepted, status(http.MethodPost, capturePath, user))
	r.Equal(http.StatusTooManyRequests, status(http.MethodPost, capturePath, user),
		"the second capture inside the minute is rate limited")
}
