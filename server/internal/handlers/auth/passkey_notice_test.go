package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// TestPasskeyRegistrationNotifiesOwner: registering a passkey from a session
// still works, and enqueues a security notice to the account owner pointing at
// the account security page (spec 2026-10-28-01).
func TestPasskeyRegistrationNotifiesOwner(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	user := models.NewUser("alice@acme.com")
	r.NoError(dbSvc.CreateUser(ctx, user))

	cfg := &config.Config{
		Auth: config.AuthConfig{
			JWTSecret: "test-secret-1234567890",
			WebAuthn:  config.WebAuthnConfig{Enabled: true},
		},
		Server: config.ServerConfig{BaseURL: passkeyTestOrigin},
	}
	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	authSvc := auth.NewService(dbSvc, cfg.Auth, cfg, jobs, nil)
	pkSvc := auth.NewPasskeyService(authSvc, dbSvc)

	// The request context RequireAuth builds for a session in org acme.
	reqCtx := context.WithValue(ctx, base.ContextKeyClaims,
		&auth.Claims{UserUID: user.UID, OrgSlug: "acme", AuthTime: time.Now().Unix()})

	device := newSoftAuthenticator(t)
	begin, err := pkSvc.BeginRegistration(reqCtx, user.UID, nil)
	r.NoError(err)

	info, err := pkSvc.FinishRegistration(reqCtx, begin.Session,
		device.register(t, begin.Options.Response.Challenge.String()), "Laptop key")
	r.NoError(err)
	r.Equal("Laptop key", info.Name)

	listed, err := dbSvc.ListJobs(ctx, nil, 0)
	r.NoError(err)

	var notices []*models.Job

	for _, job := range listed {
		if job.Type == string(jobdef.JobTypeEmail) && job.Config["template"] == email.TemplatePasskeyAdded {
			notices = append(notices, job)
		}
	}

	r.Len(notices, 1, "exactly one passkey notice")
	r.Equal([]any{user.Email}, notices[0].Config["to"])

	data, ok := notices[0].Config["templateData"].(map[string]any)
	r.True(ok, "template data present: %v", notices[0].Config)
	r.Equal("Laptop key", data["PasskeyName"])
	r.Equal(passkeyTestOrigin+config.DashboardBasePath+"/orgs/acme/account/security", data["SecurityURL"])
}
