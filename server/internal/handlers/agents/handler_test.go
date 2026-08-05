package agents_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/agents"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// TestListAllAgentsReturnsOrgAndSystemAgents covers the fleet-wide superadmin
// view: it must surface both an org-owned agent (with its org slug attached)
// and a platform-operated system agent (with no owning org), which is
// otherwise invisible to every org-scoped endpoint.
func TestListAllAgentsReturnsOrgAndSystemAgents(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	creds, err := credentials.NewService(nil, credentials.ParamStore{})
	r.NoError(err)

	svc := agents.NewService(dbSvc, creds, nil)

	// Org agent, via a private-region enrollment token.
	_, err = svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "dc1"})
	r.NoError(err)

	orgMinted, err := svc.MintEnrollmentToken(ctx, "acme", &agents.MintEnrollmentTokenRequest{RegionSlug: "dc1"}, nil)
	r.NoError(err)

	_, err = dbSvc.EnrollAgent(
		ctx, agentcrypto.HashEnrollmentToken(orgMinted.Token), "org-agent", "ed1", "age1x", "fp-org",
	)
	r.NoError(err)

	// System agent, via a platform enrollment token.
	sysToken, sysHash, err := agentcrypto.GenerateEnrollmentToken()
	r.NoError(err)
	r.NoError(dbSvc.UpsertSystemAgentEnrollmentToken(ctx,
		models.NewSystemAgentEnrollmentToken("jp-1", sysHash, time.Now().Add(time.Hour), nil)))

	_, err = dbSvc.EnrollAgent(ctx, agentcrypto.HashEnrollmentToken(sysToken), "sys-agent", "ed2", "age2x", "fp-sys")
	r.NoError(err)

	list, err := svc.ListAllAgents(ctx)
	r.NoError(err)
	r.Len(list.Data, 2)

	byName := make(map[string]agents.AgentResponse, len(list.Data))
	for _, a := range list.Data {
		byName[a.Name] = a
	}

	orgRow, ok := byName["org-agent"]
	r.True(ok)
	r.Equal(models.AgentKindOrg, orgRow.Kind)
	r.NotNil(orgRow.Org)
	r.Equal("acme", *orgRow.Org)

	sysRow, ok := byName["sys-agent"]
	r.True(ok)
	r.Equal(models.AgentKindSystem, sysRow.Kind)
	r.Nil(sysRow.Org)
}

// TestListAllAgentsHandlerAuthMatrix verifies the /api/v1/system/agents route's
// RequireSuperAdmin gate: non-superadmin users get 403, a superadmin gets 200
// with the fleet data.
func TestListAllAgentsHandlerAuthMatrix(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	creds, err := credentials.NewService(nil, credentials.ParamStore{})
	r.NoError(err)

	svc := agents.NewService(dbSvc, creds, nil)
	handler := agents.NewHandler(svc, &config.Config{})

	mkUser := func(email string, super bool) *models.User {
		u := models.NewUser(email)
		u.SuperAdmin = super
		r.NoError(dbSvc.CreateUser(ctx, u))

		return u
	}

	orgAdmin := mkUser("orgadmin@example.com", false)
	regular := mkUser("user@example.com", false)
	super := mkUser("super@example.com", true)

	authMw := middleware.NewAuthMiddleware(nil, dbSvc, &config.Config{})
	guarded := authMw.RequireSuperAdmin(handler.ListAllAgents)

	requestWithUser := func(u *models.User) *http.Request {
		c := context.WithValue(context.Background(), base.ContextKeyUser, u)

		return httptest.NewRequestWithContext(c, http.MethodGet, "/api/v1/system/agents", http.NoBody)
	}

	tests := []struct {
		name       string
		user       *models.User
		wantStatus int
	}{
		{"super admin allowed", super, http.StatusOK},
		{"org admin forbidden", orgAdmin, http.StatusForbidden},
		{"regular user forbidden", regular, http.StatusForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rr := require.New(t)
			w := httptest.NewRecorder()
			_ = guarded(w, requestWithUser(tc.user))
			rr.Equal(tc.wantStatus, w.Code)

			if tc.wantStatus == http.StatusOK {
				var resp agents.ListAgentsResponse
				rr.NoError(json.Unmarshal(w.Body.Bytes(), &resp))
				rr.Empty(resp.Data)
			}
		})
	}
}
