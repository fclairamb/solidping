package feedback

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

// TestReportUnderMCPCredentialIsAnonymous: an MCP OAuth access token is valid
// on the MCP endpoint only (spec 2026-10-16-12), so a report sent with it is
// not attributed. Positive control: a session token attributes the report.
func TestReportUnderMCPCredentialIsAnonymous(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	cfg := &config.Config{Auth: config.AuthConfig{
		JWTSecret: "test-jwt-secret", AccessTokenExpiry: time.Hour, RefreshTokenExpiry: time.Hour,
	}}
	authSvc := auth.NewService(dbSvc, cfg.Auth, cfg, nil, nil)

	user := models.NewUser("alice@acme.com")
	r.NoError(dbSvc.CreateUser(ctx, user))

	mcpToken, err := authSvc.GenerateMCPAccessToken(ctx, user.UID, "acme", []string{"mcp:read"},
		"https://solidping.test/api/v1/mcp", time.Hour, "", "dyn-123")
	r.NoError(err)
	session, err := authSvc.GenerateMCPAccessToken(ctx, user.UID, "acme", nil, "", time.Hour, "", "")
	r.NoError(err)

	h := &Handler{svc: &Service{db: dbSvc}, auth: authSvc}

	var sub SubmitReportRequest
	h.attachUserIfAuthenticated(ctx, "Bearer "+mcpToken, &sub)
	r.Empty(sub.UserUID)
	r.Empty(sub.UserEmail)

	var control SubmitReportRequest
	h.attachUserIfAuthenticated(ctx, "Bearer "+session, &control)
	r.Equal(user.UID, control.UserUID)
	r.Equal("alice@acme.com", control.UserEmail)
}
