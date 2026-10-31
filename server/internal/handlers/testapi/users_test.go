package testapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

func postUser(t *testing.T, handler *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/test/users",
		bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()

	router := httpx.New()
	router.POST("/api/v1/test/users", handler.CreateUser)
	router.ServeHTTP(rec, req)

	return rec
}

// TestCreateUser_DecoyRequest: by default the seeded user stays org-less by
// filing a pending request to a shared decoy org; personalOrg opts out of it.
func TestCreateUser_DecoyRequest(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(t.Context()))
	t.Cleanup(func() { _ = dbSvc.Close() })

	handler := NewHandler(nil, dbSvc, nil)

	// Two default users: the decoy org is created once and reused.
	for _, email := range []string{"a@acme.com", "b@acme.com"} {
		rec := postUser(t, handler, `{"email":"`+email+`","password":"supersecret123"}`)
		r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

		user, getErr := dbSvc.GetUserByEmail(t.Context(), email)
		r.NoError(getErr)

		requests, listErr := dbSvc.ListMembershipRequests(t.Context(), models.ListMembershipRequestsFilter{
			UserUID: user.UID, Status: models.MembershipRequestStatusPending, Limit: 5,
		})
		r.NoError(listErr)
		r.Len(requests, 1, email)
	}

	orgs, err := dbSvc.ListOrganizations(t.Context())
	r.NoError(err)
	r.Len(orgs, 1)
	r.Equal(decoyOrgSlug, orgs[0].Slug)

	rec := postUser(t, handler, `{"email":"c@acme.com","password":"supersecret123","personalOrg":true}`)
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	user, err := dbSvc.GetUserByEmail(t.Context(), "c@acme.com")
	r.NoError(err)

	requests, err := dbSvc.ListMembershipRequests(t.Context(), models.ListMembershipRequestsFilter{
		UserUID: user.UID, Status: models.MembershipRequestStatusPending, Limit: 5,
	})
	r.NoError(err)
	r.Empty(requests, "a personalOrg user gets no decoy request")
}

var errUsersForced = errors.New("forced storage failure")

// decoyFaultDB fails the decoy-org steps of CreateUser.
type decoyFaultDB struct {
	db.Service

	failOrgLookup bool
	failOrgCreate bool
}

func (d *decoyFaultDB) GetOrganizationBySlug(ctx context.Context, slug string) (*models.Organization, error) {
	if d.failOrgLookup {
		return nil, errUsersForced
	}

	return d.Service.GetOrganizationBySlug(ctx, slug)
}

func (d *decoyFaultDB) CreateOrganization(ctx context.Context, org *models.Organization) error {
	if d.failOrgCreate {
		return errUsersForced
	}

	return d.Service.CreateOrganization(ctx, org)
}

// TestCreateUser_DecoyFailureIs500: if the decoy request cannot be filed, the
// endpoint reports it instead of answering 201 for a user that would then
// get a personal org on sign-in.
func TestCreateUser_DecoyFailureIs500(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		fault decoyFaultDB
	}{
		{name: "decoy org lookup fails", fault: decoyFaultDB{failOrgLookup: true}},
		{name: "decoy org cannot be created", fault: decoyFaultDB{failOrgCreate: true}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
			r.NoError(err)
			r.NoError(dbSvc.Initialize(t.Context()))
			t.Cleanup(func() { _ = dbSvc.Close() })

			fault := tc.fault
			fault.Service = dbSvc

			rec := postUser(t, NewHandler(nil, &fault, nil), `{"email":"a@acme.com","password":"supersecret123"}`)
			r.Equal(http.StatusInternalServerError, rec.Code, rec.Body.String())
		})
	}
}
