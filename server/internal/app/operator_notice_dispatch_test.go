package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
)

// newOperatorNoticeServer boots a real server — the production wiring, not a
// hand-assembled one — so the dispatcher under test is the one SetupRoutes
// installs.
func newOperatorNoticeServer(t *testing.T) (*Server, context.Context) {
	t.Helper()

	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "operator-notice-dispatch-secret"
	cfg.Auth.AccessTokenExpiry = time.Hour
	cfg.Auth.RefreshTokenExpiry = 24 * time.Hour
	cfg.FileStorage.Type = "local"
	cfg.FileStorage.LocalRoot = t.TempDir()

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	r.NoError(server.Initialize(ctx))
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	// SetupRoutes is what installs the process-wide notice dispatcher.
	server.SetupRoutes(ctx)
	t.Cleanup(func() { opsnotify.SetDispatcher(nil) })

	// Registration is otherwise refused, and this test is about what happens
	// AFTER an account is created.
	server.config.Auth.RegistrationEmailPattern = ".*"

	return server, ctx
}

// operatorNoticeJobs reads the queued notices straight from the table: they are
// enqueued with no organization, so an org-scoped listing would miss them.
func operatorNoticeJobs(t *testing.T, server *Server) []jobtypes.OperatorNoticeJobConfig {
	t.Helper()

	var jobs []*models.Job

	require.NoError(t, server.dbService.DB().NewSelect().
		Model(&jobs).
		Where("type = ?", string(jobdef.JobTypeOperatorNotice)).
		Where("deleted_at IS NULL").
		Scan(t.Context()))

	out := make([]jobtypes.OperatorNoticeJobConfig, 0, len(jobs))

	for _, job := range jobs {
		raw, err := json.Marshal(job.Config)
		require.NoError(t, err)

		cfg := jobtypes.OperatorNoticeJobConfig{}
		require.NoError(t, json.Unmarshal(raw, &cfg))
		out = append(out, cfg)
	}

	return out
}

// signUp drives a real password registration through the server's own auth
// service and returns the created user.
func signUp(t *testing.T, server *Server, ctx context.Context, email string) *models.User {
	t.Helper()

	r := require.New(t)

	_, err := server.authService.Register(ctx, auth.RegisterRequest{
		Name: "Alice", Email: email, Password: "supersecret123",
	})
	r.NoError(err)

	entries, err := server.dbService.ListStateEntries(ctx, nil, "")
	r.NoError(err)

	var token string

	for _, entry := range entries {
		if entry.Value == nil {
			continue
		}

		if got, ok := (*entry.Value)["email"].(string); ok && got == email {
			token, _ = (*entry.Value)["token"].(string)
		}
	}

	r.NotEmpty(token, "precondition: the registration token must have been stored")

	_, err = server.authService.ConfirmRegistration(ctx, token)
	r.NoError(err)

	user, err := server.dbService.GetUserByEmail(ctx, email)
	r.NoError(err)
	r.NotNil(user)

	return user
}

// TestSignupEnqueuesANoticeCarryingTheNewUser closes the seam the audit found
// open: every earlier test sat on one side or the other of the dispatcher, so
// the production wiring could drop AboutUserUID (and with it the landing
// organization every notice is supposed to carry) without a single failure.
//
// This drives a REAL signup through the REAL dispatcher and asserts what
// actually landed in the job table.
func TestSignupEnqueuesANoticeCarryingTheNewUser(t *testing.T) {
	//nolint:paralleltest // installs the process-wide notice dispatcher
	r := require.New(t)
	server, ctx := newOperatorNoticeServer(t)

	const email = "dispatch-seam@acme.com"

	user := signUp(t, server, ctx, email)

	notices := operatorNoticeJobs(t, server)
	r.Len(notices, 1, "one account created is one queued notice")
	r.Equal(opsnotify.EventUserRegistered, notices[0].Event)
	r.Contains(notices[0].Subject, email)
	r.Equal(user.UID, notices[0].AboutUserUID,
		"without this the delivering job cannot resolve the landing organization")
}

// TestQueuedSignupNoticeResolvesItsOrganization runs the whole chain the way
// production does — signup, dispatcher, queued job, delivery — and asserts the
// operator's email actually names the organization. It is the end-to-end proof
// that the field above is not merely present but load-bearing.
func TestQueuedSignupNoticeResolvesItsOrganization(t *testing.T) {
	//nolint:paralleltest // installs the process-wide notice dispatcher
	r := require.New(t)
	server, ctx := newOperatorNoticeServer(t)

	// An organization whose auto-join pattern admits the new account, so the
	// signup genuinely lands somewhere.
	org := models.NewOrganization("acme", "Acme")
	r.NoError(server.dbService.CreateOrganization(ctx, org))
	r.NoError(server.dbService.SetOrgParameter(ctx, org.UID, "registration.email_pattern", ".*@acme\\.com$", false))

	// A super admin subscribed to signups, reachable by email.
	admin := models.NewUser("operator@acme.com")
	admin.SuperAdmin = true
	r.NoError(server.dbService.CreateUser(ctx, admin))
	r.NoError(server.dbService.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, admin.UID, models.MemberRoleAdmin)))
	r.NoError(server.dbService.EnsureDefaultEmailRoute(ctx, admin.UID, org.UID, admin.Email))

	r.NoError(server.dbService.SetSystemParameter(ctx, opsnotify.ParamOperatorNotifications, map[string]any{
		"enabled": true,
		"recipients": []map[string]any{
			{"userUid": admin.UID, "events": []string{opsnotify.EventUserRegistered}},
		},
	}, false))

	signUp(t, server, ctx, "newcomer@acme.com")

	queued := operatorNoticeJobs(t, server)
	r.Len(queued, 1)

	// Run the queued notice through the real job.
	raw, err := json.Marshal(queued[0])
	r.NoError(err)

	runner, err := (&jobtypes.OperatorNoticeJobDefinition{}).CreateJobRun(raw)
	r.NoError(err)
	r.NoError(runner.Run(ctx, &jobdef.JobContext{
		Services:  server.services,
		DB:        server.dbService.DB(),
		DBService: server.dbService,
		AppConfig: server.config,
		Logger:    slog.Default(),
	}))

	var emails []*models.Job

	r.NoError(server.dbService.DB().NewSelect().
		Model(&emails).
		Where("type = ?", string(jobdef.JobTypeEmail)).
		Where("deleted_at IS NULL").
		Scan(ctx))

	var bodies string

	for _, job := range emails {
		raw, marshalErr := json.Marshal(job.Config)
		r.NoError(marshalErr)

		bodies += string(raw)
	}

	r.Contains(bodies, "newcomer@acme.com", "precondition: the notice email went out")
	r.Contains(bodies, "Org:", "the operator is told which organization the signup landed in")
	r.Contains(bodies, org.Slug)
}
