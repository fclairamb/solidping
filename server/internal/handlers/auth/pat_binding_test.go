package auth

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portPATBindingPG is distinct from every other embedded-Postgres port
// claimed in the repo (see the port-numbering note in
// internal/db/incident_number_test.go).
const portPATBindingPG = 15673

// patBindingFixture: one user, admin of org A and org B, holding a PAT created
// in org A (spec 2026-10-28-01).
type patBindingFixture struct {
	svc    *Service
	db     db.Service
	orgA   *models.Organization
	orgB   *models.Organization
	user   *models.User
	claims *Claims
	patCtx context.Context
}

func newPATBindingFixture(
	ctx context.Context, t *testing.T, svc *Service, dbSvc db.Service, suffix string,
) *patBindingFixture {
	t.Helper()
	r := require.New(t)

	orgA := models.NewOrganization("pata"+suffix, "Acme A "+suffix)
	r.NoError(dbSvc.CreateOrganization(ctx, orgA))
	orgB := models.NewOrganization("patb"+suffix, "Acme B "+suffix)
	r.NoError(dbSvc.CreateOrganization(ctx, orgB))

	user := models.NewUser("alice-" + suffix + "@acme.com")
	r.NoError(dbSvc.CreateUser(ctx, user))

	now := time.Now()
	for _, org := range []*models.Organization{orgA, orgB} {
		member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
		member.JoinedAt = &now
		r.NoError(dbSvc.CreateOrganizationMember(ctx, member))
	}

	pat, err := svc.CreatePAT(ctx, orgA.Slug, user.UID, CreateTokenRequest{Name: "ci"})
	r.NoError(err)

	claims, err := svc.ValidateToken(ctx, pat.Token)
	r.NoError(err)
	r.True(claims.IsPAT())
	r.Equal(orgA.Slug, claims.OrgSlug)

	return &patBindingFixture{
		svc: svc, db: dbSvc, orgA: orgA, orgB: orgB, user: user, claims: claims,
		patCtx: WithPATCredential(ctx),
	}
}

func (f *patBindingFixture) tokenCount(ctx context.Context, t *testing.T, tokenType models.TokenType) int {
	t.Helper()

	tokens, err := f.db.ListUserTokensByType(ctx, f.user.UID, tokenType)
	require.NoError(t, err)

	return len(tokens)
}

// runPATBindingContract is the service half of the PAT binding, shared by the
// SQLite test and its Postgres twin. The route half (RequireAuth's denylist)
// is covered against the real route table in internal/app.
func runPATBindingContract(t *testing.T, newFixture func(t *testing.T) (*patBindingFixture, context.Context)) {
	t.Helper()

	t.Run("switch-org from a PAT mints no session", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		f, ctx := newFixture(t)

		before := f.tokenCount(ctx, t, models.TokenTypeRefresh)

		_, err := f.svc.SwitchOrg(f.patCtx, f.user.UID, f.orgB.Slug, Context{})
		r.ErrorIs(err, ErrPATForbidden)
		r.Equal(before, f.tokenCount(ctx, t, models.TokenTypeRefresh), "no refresh token was minted")

		// Positive control: the same call from an interactive session works.
		resp, err := f.svc.SwitchOrg(ctx, f.user.UID, f.orgB.Slug, Context{})
		r.NoError(err)
		r.NotEmpty(resp.RefreshToken)
		r.Equal(before+1, f.tokenCount(ctx, t, models.TokenTypeRefresh))
	})

	t.Run("device consent from a PAT mints no PAT", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		f, ctx := newFixture(t)

		before := f.tokenCount(ctx, t, models.TokenTypePAT)

		start, err := f.svc.StartDeviceAuthorization(ctx, "sp CLI", DeviceRequester{IP: "203.0.113.7"})
		r.NoError(err)

		err = f.svc.RespondToDeviceConsent(f.patCtx, f.claims, start.UserCode, f.orgB.Slug, true)
		r.ErrorIs(err, ErrPATForbidden)
		r.Equal(before, f.tokenCount(ctx, t, models.TokenTypePAT), "no PAT was minted")

		// Positive control: a session approves the same request.
		session := &Claims{UserUID: f.user.UID, OrgSlug: f.orgA.Slug}
		r.NoError(f.svc.RespondToDeviceConsent(ctx, session, start.UserCode, f.orgB.Slug, true))
		r.Equal(before+1, f.tokenCount(ctx, t, models.TokenTypePAT))
	})

	t.Run("a PAT cannot change the sign-in email", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		f, ctx := newFixture(t)

		newEmail := "mallory-" + f.orgA.Slug + "@acme.com"
		_, err := f.svc.UpdateProfile(f.patCtx, f.claims, UpdateProfileRequest{Email: &newEmail})
		r.ErrorIs(err, ErrPATForbidden)

		user, err := f.db.GetUser(ctx, f.user.UID)
		r.NoError(err)
		r.Equal(f.user.Email, user.Email)

		// The rest of the profile stays editable with a PAT.
		name := "Alice"
		_, err = f.svc.UpdateProfile(f.patCtx, f.claims, UpdateProfileRequest{Name: &name})
		r.NoError(err)
	})

	t.Run("a PAT still creates a PAT in its own org", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		f, _ := newFixture(t)

		created, err := f.svc.CreatePAT(f.patCtx, f.orgA.Slug, f.user.UID, CreateTokenRequest{Name: "another"})
		r.NoError(err)

		claims, err := f.svc.ValidateToken(f.patCtx, created.Token)
		r.NoError(err)
		r.Equal(f.orgA.Slug, claims.OrgSlug)
	})

	t.Run("startSession refuses a PAT request", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		f, ctx := newFixture(t)

		before := f.tokenCount(ctx, t, models.TokenTypeRefresh)
		row := &models.UserToken{UID: "pat-session-" + f.orgA.Slug, UserUID: f.user.UID, Type: models.TokenTypeRefresh}

		err := f.svc.startSession(f.patCtx, sessionActor{UID: f.user.UID, Email: f.user.Email},
			f.orgA, row, string(models.MemberRoleAdmin), "password", Context{})
		r.ErrorIs(err, ErrPATForbidden)
		r.Equal(before, f.tokenCount(ctx, t, models.TokenTypeRefresh))
	})
}

func TestPATBinding(t *testing.T) {
	t.Parallel()

	var seq atomic.Int64

	runPATBindingContract(t, func(t *testing.T) (*patBindingFixture, context.Context) {
		t.Helper()

		ctx := t.Context()
		dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
		require.NoError(t, err)
		require.NoError(t, dbSvc.Initialize(ctx))
		t.Cleanup(func() { _ = dbSvc.Close() })

		cfg := impersonationTestConfig()
		svc := NewService(dbSvc, cfg.Auth, cfg, nil, nil)

		return newPATBindingFixture(ctx, t, svc, dbSvc, "s"+strconv.FormatInt(seq.Add(1), 10)), ctx
	})
}

// TestPATBinding_Postgres is the real-engine twin of TestPATBinding.
func TestPATBinding_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbService, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portPATBindingPG,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbService.Close() })

	if initErr := dbService.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	var seq atomic.Int64

	runPATBindingContract(t, func(t *testing.T) (*patBindingFixture, context.Context) {
		t.Helper()

		cfg := impersonationTestConfig()
		svc := NewService(dbService, cfg.Auth, cfg, nil, nil)
		subCtx := t.Context()

		return newPATBindingFixture(subCtx, t, svc, dbService, "p"+strconv.FormatInt(seq.Add(1), 10)), subCtx
	})
}
