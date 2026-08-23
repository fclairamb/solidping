package supportinbox_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/supportinbox"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/support"
)

// operation is one endpoint of the support API, named the way the spec lists it.
type operation struct {
	name   string
	method string
	path   string
	body   string
	// wantSuperAdmin is what a super admin should get. It differs per verb
	// (201 for a successful reply), and asserting the REAL success code rather
	// than "anything that is not 401/403" is what makes this a positive control
	// instead of a tautology.
	wantSuperAdmin int
}

// TestSupportRoutesRequireSuperAdmin drives the REAL route registration —
// supportinbox.RegisterRoutes, the same function server.go calls — through a
// live HTTP server, with real JWTs.
//
// This is the gate that actually protects the data. The dash0 `/support` route
// is unlinked, but absence from a menu only affects discoverability: the URL is
// public knowledge, and these endpoints expose every inbound human message on
// the instance, from senders who are frequently strangers. The Playwright 403
// test mocks /api/v1/auth/me and therefore exercises only the CLIENT gate — if
// the middleware were dropped here, nothing else in the suite would notice.
func TestSupportRoutesRequireSuperAdmin(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	authCfg := config.AuthConfig{
		JWTSecret:          "support-routes-test-secret",
		AccessTokenExpiry:  time.Hour,
		RefreshTokenExpiry: 7 * 24 * time.Hour,
	}
	fullCfg := &config.Config{Auth: authCfg}
	authService := auth.NewService(dbSvc, authCfg, fullCfg, nil, nil)
	authMW := middleware.NewAuthMiddleware(authService, dbSvc, fullCfg)

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	mkUser := func(email string, superAdmin bool) string {
		user := models.NewUser(email)
		pwd := "$plaintext$pw"
		user.PasswordHash = &pwd
		user.SuperAdmin = superAdmin
		r.NoError(dbSvc.CreateUser(ctx, user))
		// An org ADMIN, deliberately: the point is that even the highest
		// org-level role is not enough for an instance-level inbox.
		r.NoError(dbSvc.CreateOrganizationMember(
			ctx, models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

		login, loginErr := authService.Login(ctx, org.Slug, email, "pw", auth.Context{})
		r.NoError(loginErr)

		return login.AccessToken
	}

	superToken := mkUser("super@acme.com", true)
	adminToken := mkUser("admin@acme.com", false)

	// A real thread so the per-thread routes resolve rather than 404ing before
	// the gate has anything to prove.
	inbox := support.NewService(dbSvc, support.Options{BaseURL: "https://solidping.example"})
	inbox.RegisterReplier(models.SupportChannelTelegram,
		func(_ context.Context, _ *models.SupportThread, _ string) (string, error) {
			return "sent-1", nil
		})

	thread, _, err := inbox.Capture(ctx, &support.Inbound{
		Channel: models.SupportChannelTelegram, Identity: "42",
		ExternalID: "42:1", Body: "is the api down?",
	})
	r.NoError(err)

	router := httpx.New()
	api := router.NewGroup("/api/v1")
	supportinbox.RegisterRoutes(api, authMW, supportinbox.NewHandler(inbox, fullCfg))

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	base := "/api/v1/support/threads"
	operations := []operation{
		{"list threads", http.MethodGet, base, "", http.StatusOK},
		{"get thread", http.MethodGet, base + "/" + thread.UID, "", http.StatusOK},
		{"patch thread", http.MethodPatch, base + "/" + thread.UID, `{"status":"pending"}`, http.StatusOK},
		{"list messages", http.MethodGet, base + "/" + thread.UID + "/messages", "", http.StatusOK},
		{
			"send reply", http.MethodPost, base + "/" + thread.UID + "/messages",
			`{"body":"on it"}`, http.StatusCreated,
		},
	}

	call := func(t *testing.T, op operation, token string) int {
		t.Helper()

		rr := require.New(t)

		var body *strings.Reader
		if op.body != "" {
			body = strings.NewReader(op.body)
		} else {
			body = strings.NewReader("")
		}

		req, reqErr := http.NewRequestWithContext(t.Context(), op.method, server.URL+op.path, body)
		rr.NoError(reqErr)
		req.Header.Set("Content-Type", "application/json")

		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, doErr := http.DefaultClient.Do(req)
		rr.NoError(doErr)
		t.Cleanup(func() { _ = resp.Body.Close() })

		return resp.StatusCode
	}

	t.Run("gate rejects everyone below super admin", func(t *testing.T) {
		t.Parallel()

		for _, op := range operations {
			require.Equal(t, http.StatusUnauthorized, call(t, op, ""),
				"unauthenticated must be 401 on %s", op.name)
			require.Equal(t, http.StatusForbidden, call(t, op, adminToken),
				"an org admin must not reach the instance support inbox: %s", op.name)
		}
	})

	// POSITIVE CONTROL: a super admin gets through every one of them. Without
	// this, the 401/403 assertions above would pass just as happily on a route
	// table that 404s or on a handler that is broken.
	//
	// Sequential within the subtest, and after the rejections, because these
	// mutate one thread: the PATCH sets pending, the POST appends a reply.
	t.Run("super admin reaches every operation", func(t *testing.T) {
		t.Parallel()

		for _, op := range operations {
			require.Equal(t, op.wantSuperAdmin, call(t, op, superToken),
				"super admin should reach %s", op.name)
		}
	})
}
