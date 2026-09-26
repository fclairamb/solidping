package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/testsupport"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// portLogoutSessionPG is distinct from every other _postgres_test.go /
// embedded-postgres port used elsewhere in this package (provider_links:
// 15501, token_hash_at_rest: 15561, handoff_exchange: 15549).
const portLogoutSessionPG = 15562

// logoutSessionTestPassword is the fixed password used by every
// logoutSessionFixture-seeded user in this file.
const logoutSessionTestPassword = "testpass1234"

// logoutSessionFixture seeds an org plus a password user that belongs to it,
// so tests can log in for real claims.
//
//nolint:revive // ctx-second matches the existing helpers in this package
func logoutSessionFixture(
	t *testing.T, ctx context.Context, dbSvc db.Service, slug, email string,
) *models.User {
	t.Helper()
	r := require.New(t)

	org := models.NewOrganization(slug, slug)
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	hash, err := passwords.Hash(logoutSessionTestPassword)
	r.NoError(err)

	user := models.NewUser(email)
	user.PasswordHash = &hash
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	return user
}

// postLogout invokes the HTTP handler directly (mirrors the RevokeCurrentToken
// test pattern) so the assertions exercise cookie-clearing and JSON shape,
// not just the service layer.
//
//nolint:revive // ctx-second matches the existing helpers in this package
func postLogout(
	t *testing.T, ctx context.Context, handler *Handler, claims *Claims, body string,
) *httptest.ResponseRecorder {
	t.Helper()

	reqCtx := context.WithValue(ctx, base.ContextKeyClaims, claims)

	var bodyReader *bytes.Reader
	if body == "" {
		bodyReader = bytes.NewReader(nil)
	} else {
		bodyReader = bytes.NewReader([]byte(body))
	}

	httpReq := httptest.NewRequestWithContext(reqCtx, http.MethodPost, "/api/v1/auth/logout", bodyReader)
	rec := httptest.NewRecorder()

	require.NoError(t, handler.Logout(rec, httpReq))

	return rec
}

// cookieCleared asserts the response cleared the access-token cookie
// (MaxAge < 0, matching Handler.clearAuthCookie).
func cookieCleared(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	r := require.New(t)

	cookies := rec.Result().Cookies()
	r.Len(cookies, 1)
	r.Equal(CookieAuthToken, cookies[0].Name)
	r.Negative(cookies[0].MaxAge, "logout must clear the access-token cookie")
}

// runLogoutDeletesSessionRowCase is the core acceptance criterion (spec
// 2026-09-25-24), exercised on both engines: the default POST /auth/logout
// path must delete the refresh-token row named by claims.RefreshUID, not
// just clear the cookie — otherwise a captured refresh token (also returned
// in the login JSON body) keeps a live sliding session after "logout".
func runLogoutDeletesSessionRowCase(t *testing.T, svc *Service, dbSvc db.Service) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	user := logoutSessionFixture(t, ctx, dbSvc, "logout-row-org", "logout-row@example.com")

	loginResp, err := svc.Login(ctx, "logout-row-org", "logout-row@example.com", logoutSessionTestPassword, Context{})
	r.NoError(err)

	claims, err := svc.ValidateToken(ctx, loginResp.AccessToken)
	r.NoError(err)
	r.NotEmpty(claims.RefreshUID)

	// The row exists before logout.
	_, err = dbSvc.GetUserTokenByToken(ctx, loginResp.RefreshToken)
	r.NoError(err)

	handler := NewHandler(svc, &config.Config{})
	rec := postLogout(t, ctx, handler, claims, "")
	r.Equal(http.StatusOK, rec.Code)
	cookieCleared(t, rec)

	// The session row is gone…
	_, err = dbSvc.GetUserToken(ctx, claims.RefreshUID)
	r.Error(err, "the session row must be deleted on logout")

	// …and using the captured refresh token to refresh must now fail.
	_, err = svc.Refresh(ctx, loginResp.RefreshToken)
	r.ErrorIs(err, ErrInvalidToken)

	// The user still has zero sessions (not soft-orphaned).
	sessions, err := dbSvc.ListUserTokensByType(ctx, user.UID, models.TokenTypeRefresh)
	r.NoError(err)
	r.Empty(sessions)
}

func TestLogoutDeletesSessionRow_SQLite(t *testing.T) {
	t.Parallel()

	svc, dbSvc, _ := setupAuthTestService(t)
	runLogoutDeletesSessionRowCase(t, svc, dbSvc)
}

//nolint:paralleltest // one embedded PG instance for this test
func TestLogoutDeletesSessionRow_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portLogoutSessionPG,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	authCfg := config.AuthConfig{
		JWTSecret:          "test-jwt-secret",
		AccessTokenExpiry:  time.Hour,
		RefreshTokenExpiry: 7 * 24 * time.Hour,
	}
	svc := NewService(dbSvc, authCfg, &config.Config{Auth: authCfg}, nil, nil)

	runLogoutDeletesSessionRowCase(t, svc, dbSvc)
}

// TestLogoutWithoutRefreshUIDLeavesPATIntact covers the PAT-hitting-/logout
// case (Proposal item 2): no RefreshUID means no session row to delete —
// behave exactly as before, clear the cookie, and never touch the PAT row.
func TestLogoutWithoutRefreshUIDLeavesPATIntact(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	user := logoutSessionFixture(t, ctx, dbSvc, "logout-pat-org", "logout-pat@example.com")

	patResp, err := svc.CreatePAT(ctx, "logout-pat-org", user.UID, CreateTokenRequest{Name: "test-pat"})
	r.NoError(err)

	patClaims, err := svc.ValidateToken(ctx, patResp.Token)
	r.NoError(err)
	r.Empty(patClaims.RefreshUID, "PAT-validated claims must not carry a refreshUid")

	handler := NewHandler(svc, &config.Config{})
	rec := postLogout(t, ctx, handler, patClaims, "")
	r.Equal(http.StatusOK, rec.Code)
	cookieCleared(t, rec)

	// The PAT row must be untouched and still valid.
	pats, err := dbSvc.ListUserTokensByType(ctx, user.UID, models.TokenTypePAT)
	r.NoError(err)
	r.Len(pats, 1, "logout without a RefreshUID must not delete anything")

	revalidated, err := svc.ValidateToken(ctx, patResp.Token)
	r.NoError(err)
	r.Equal(patClaims.UserUID, revalidated.UserUID)
}

// errDeleteUserTokenForced is returned by failingDeleteUserTokenDB.DeleteUserToken.
var errDeleteUserTokenForced = errors.New("forced DeleteUserToken failure")

// failingDeleteUserTokenDB embeds a real db.Service but forces
// DeleteUserToken to fail, so the "row delete errors" failure-handling path
// (Proposal item 3) can be exercised without a real DB outage.
type failingDeleteUserTokenDB struct {
	db.Service
}

func (failingDeleteUserTokenDB) DeleteUserToken(_ context.Context, _ string) (bool, error) {
	return false, errDeleteUserTokenForced
}

// logCapture is a concurrency-safe io.Writer backing a slog handler, so a
// test can assert on what the code under test actually logged.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.buf.Write(p)
}

func (c *logCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.buf.String()
}

// captureDefaultLogs swaps slog's default logger for one writing everything
// (down to Debug) into the returned capture, restoring the original on
// cleanup.
//
// Callers must NOT be parallel: slog's default logger is process-global.
func captureDefaultLogs(t *testing.T) *logCapture {
	t.Helper()

	capture := &logCapture{}
	previous := slog.Default()

	slog.SetDefault(slog.New(slog.NewTextHandler(capture, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return capture
}

// TestLogoutDeleteFailureStillClearsCookieAndLogsError is the failure-handling
// acceptance criterion (Proposal item 3): a failed row delete must not trap
// the user in a logged-in UI — the response is still 200 with the cookie
// cleared, but the failure is logged at ERROR with the refresh UID.
//
//nolint:paralleltest // Swaps the process-global slog default logger.
func TestLogoutDeleteFailureStillClearsCookieAndLogsError(t *testing.T) {
	r := require.New(t)

	capture := captureDefaultLogs(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	logoutSessionFixture(t, ctx, dbSvc, "logout-fail-org", "logout-fail@example.com")

	loginResp, err := svc.Login(ctx, "logout-fail-org", "logout-fail@example.com", logoutSessionTestPassword, Context{})
	r.NoError(err)

	claims, err := svc.ValidateToken(ctx, loginResp.AccessToken)
	r.NoError(err)
	r.NotEmpty(claims.RefreshUID)

	// Wrap the same underlying DB (row already exists) so DeleteUserToken
	// fails but everything else (the ownership lookup) still works for real.
	failingSvc := NewService(failingDeleteUserTokenDB{Service: dbSvc}, svc.cfg, svc.fullCfg, nil, nil)

	handler := NewHandler(failingSvc, &config.Config{})
	rec := postLogout(t, ctx, handler, claims, "")

	r.Equal(http.StatusOK, rec.Code, "a failed session delete must still return 200")
	cookieCleared(t, rec)

	logs := capture.String()
	r.Contains(logs, "Failed to delete session on logout")
	r.Contains(logs, "level=ERROR")
	r.Contains(logs, claims.RefreshUID, "the log must name the refresh UID that failed to delete")

	// The row survives the failed delete.
	_, err = dbSvc.GetUserToken(ctx, claims.RefreshUID)
	r.NoError(err, "a failed delete must leave the row in place")
}

// TestLogoutDeleteAllTokensStillWorks pins the "deleteAllTokens path
// unchanged" acceptance criterion at the handler level: the default-path
// change must not have touched this branch.
func TestLogoutDeleteAllTokensStillWorks(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	user := logoutSessionFixture(t, ctx, dbSvc, "logout-all-org", "logout-all@example.com")

	current, err := svc.Login(ctx, "logout-all-org", "logout-all@example.com", logoutSessionTestPassword, Context{})
	r.NoError(err)
	_, err = svc.Login(ctx, "logout-all-org", "logout-all@example.com", logoutSessionTestPassword, Context{})
	r.NoError(err)

	claims, err := svc.ValidateToken(ctx, current.AccessToken)
	r.NoError(err)

	handler := NewHandler(svc, &config.Config{})
	rec := postLogout(t, ctx, handler, claims, `{"deleteAllTokens":true}`)
	r.Equal(http.StatusOK, rec.Code)
	cookieCleared(t, rec)

	sessions, err := dbSvc.ListUserTokensByType(ctx, user.UID, models.TokenTypeRefresh)
	r.NoError(err)
	r.Empty(sessions, "deleteAllTokens must still remove every session")
}
