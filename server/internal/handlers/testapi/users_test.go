package testapi

import (
	"bytes"
	"context"
	"encoding/json"
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

var errInjectedOrgFailure = errors.New("injected organization failure")

// failingOrgDB refuses to create organizations.
type failingOrgDB struct {
	db.Service
}

func (failingOrgDB) CreateOrganization(context.Context, *models.Organization) error {
	return errInjectedOrgFailure
}

func newUsersHandler(t *testing.T, wrap func(db.Service) db.Service) (*Handler, db.Service) {
	t.Helper()

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(t.Context()))
	t.Cleanup(func() { _ = dbSvc.Close() })

	var svc db.Service = dbSvc
	if wrap != nil {
		svc = wrap(dbSvc)
	}

	return NewHandler(nil, svc, nil), dbSvc
}

func postUser(t *testing.T, handler *Handler, body any) *httptest.ResponseRecorder {
	t.Helper()

	raw, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/test/users", bytes.NewReader(raw))
	w := httptest.NewRecorder()

	router := httpx.New()
	router.POST("/api/v1/test/users", handler.CreateUser)
	router.ServeHTTP(w, req)

	return w
}

// TestCreateUser_RetiresThePersonalOrgByDefault: the seeded identity is
// marked as having had (and deleted) its one personal org, so its first
// sign-in stays on /no-org.
func TestCreateUser_RetiresThePersonalOrgByDefault(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	handler, dbSvc := newUsersHandler(t, nil)

	w := postUser(t, handler, map[string]any{"email": "zero@example.com", "password": "secret123", "name": "Zero"})
	r.Equal(http.StatusCreated, w.Code, w.Body.String())

	user, err := dbSvc.GetUserByEmail(t.Context(), "zero@example.com")
	r.NoError(err)

	retired, err := dbSvc.GetAutoPersonalOrgByOwner(t.Context(), user.UID, true)
	r.NoError(err)
	r.NotNil(retired)
	r.NotNil(retired.DeletedAt)

	live, err := dbSvc.GetAutoPersonalOrgByOwner(t.Context(), user.UID, false)
	r.NoError(err)
	r.Nil(live, "the retired org is not a live one")
}

// TestCreateUser_PersonalOrgOptIn: personalOrg=true leaves the user as a
// real new user, with no retired org on record.
func TestCreateUser_PersonalOrgOptIn(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	handler, dbSvc := newUsersHandler(t, nil)

	w := postUser(t, handler, map[string]any{
		"email": "fresh@example.com", "password": "secret123", "personalOrg": true,
	})
	r.Equal(http.StatusCreated, w.Code, w.Body.String())

	user, err := dbSvc.GetUserByEmail(t.Context(), "fresh@example.com")
	r.NoError(err)

	none, err := dbSvc.GetAutoPersonalOrgByOwner(t.Context(), user.UID, true)
	r.NoError(err)
	r.Nil(none)
}

func TestCreateUser_Refusals(t *testing.T) {
	t.Parallel()

	handler, _ := newUsersHandler(t, nil)

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"missing email", map[string]any{"password": "secret123"}, http.StatusBadRequest},
		{"missing password", map[string]any{"email": "a@example.com"}, http.StatusBadRequest},
	}

	for _, tc := range cases {
		require.Equal(t, tc.want, postUser(t, handler, tc.body).Code, tc.name)
	}

	good := map[string]any{"email": "dup@example.com", "password": "secret123"}
	require.Equal(t, http.StatusCreated, postUser(t, handler, good).Code)
	require.Equal(t, http.StatusConflict, postUser(t, handler, good).Code, "email already taken")

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/test/users", bytes.NewReader([]byte("{")))
	w := httptest.NewRecorder()
	router := httpx.New()
	router.POST("/api/v1/test/users", handler.CreateUser)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "malformed body")
}

// TestCreateUser_RetireFailure: a database that cannot record the retired
// org fails the request.
func TestCreateUser_RetireFailure(t *testing.T) {
	t.Parallel()

	handler, _ := newUsersHandler(t, func(s db.Service) db.Service { return failingOrgDB{Service: s} })

	w := postUser(t, handler, map[string]any{"email": "boom@example.com", "password": "secret123"})
	require.Equal(t, http.StatusInternalServerError, w.Code)
}
