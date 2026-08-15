package ovhsmscb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

const testToken = "9f2c1d4e6a8b0c2d4e6f8a0b2c4d6e8f"

// dlrEnv seeds a DB with one sent SMS notification whose message id is the OVH
// job id a receipt will quote back.
type dlrEnv struct {
	db      db.Service
	handler http.Handler
	orgUID  string
	notifID string
}

func setupDLR(t *testing.T, token string) *dlrEnv {
	t.Helper()

	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("dlr-org", "DLR Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("oncall@example.com")
	require.NoError(t, dbSvc.CreateUser(ctx, user))

	check := &models.Check{UID: "check-1", OrganizationUID: org.UID, Type: "http"}
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	incident := &models.Incident{
		UID: "incident-1", OrganizationUID: org.UID, CheckUID: check.UID,
		State: models.IncidentStateActive, StartedAt: time.Now(),
	}
	require.NoError(t, dbSvc.CreateIncident(ctx, incident))

	notif := models.NewIncidentNotificationForUser(
		org.UID, incident.UID, string(models.EventTypeIncidentEscalated),
		models.IncidentNotificationSourceEscalationUser, user.UID, "sms", nil, nil,
	)
	require.NoError(t, dbSvc.CreateIncidentNotification(ctx, notif))
	require.NoError(t, dbSvc.MarkIncidentNotificationSentByUID(ctx, notif.UID, time.Now(), "424242"))

	handler := NewHandler(dbSvc, token)

	router := chi.NewRouter()
	router.Get("/api/v1/integrations/ovhsms/dlr/{token}", func(w http.ResponseWriter, r *http.Request) {
		_ = handler.HandleDLR(w, r)
	})
	router.Post("/api/v1/integrations/ovhsms/dlr/{token}", func(w http.ResponseWriter, r *http.Request) {
		_ = handler.HandleDLR(w, r)
	})

	return &dlrEnv{db: dbSvc, handler: router, orgUID: org.UID, notifID: notif.UID}
}

func (e *dlrEnv) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody))

	return rec
}

func (e *dlrEnv) deliveryBody(t *testing.T) string {
	t.Helper()

	row, err := e.db.GetOrgNotification(t.Context(), e.orgUID, e.notifID)
	require.NoError(t, err)

	if row == nil || row.DeliveryDetails == nil {
		return ""
	}

	return row.DeliveryDetails.ResponseBody
}

// TestValidTokenRecordsDelivery is the happy path: the right token, a known
// job id, a delivered ptt.
func TestValidTokenRecordsDelivery(t *testing.T) {
	t.Parallel()

	env := setupDLR(t, testToken)

	rec := env.get(t, "/api/v1/integrations/ovhsms/dlr/"+testToken+"?id=424242&ptt=4")
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Contains(t, env.deliveryBody(t), "delivered")
}

// TestWrongTokenIs404 is the security control. An unknown token must 404 with
// no body — never a 401 with detail, which would confirm the endpoint exists
// and invite probing.
func TestWrongTokenIs404(t *testing.T) {
	t.Parallel()

	env := setupDLR(t, testToken)

	// Single-segment tokens reach the handler itself; the empty and traversal
	// forms never match the route at all. Both must be a bare 404.
	for _, bad := range []string{"wrong", testToken + "x", testToken[:len(testToken)-1]} {
		rec := env.get(t, "/api/v1/integrations/ovhsms/dlr/"+bad+"?id=424242&ptt=4")
		require.Equal(t, http.StatusNotFound, rec.Code, "token %q must 404", bad)
		require.Empty(t, rec.Body.String(),
			"a rejection must leak no detail — never a 401 with a reason")
	}

	for _, bad := range []string{"", "../../etc/passwd"} {
		rec := env.get(t, "/api/v1/integrations/ovhsms/dlr/"+bad+"?id=424242&ptt=4")
		require.Equal(t, http.StatusNotFound, rec.Code, "token %q must 404", bad)
	}

	// And nothing was written.
	require.Empty(t, env.deliveryBody(t))
}

// TestUnconfiguredTokenRejectsEverything proves a deployment with no OVH
// provider serves a route that can never be satisfied, rather than one that
// accepts an empty token.
func TestUnconfiguredTokenRejectsEverything(t *testing.T) {
	t.Parallel()

	env := setupDLR(t, "")

	for _, candidate := range []string{"", "anything", testToken} {
		rec := env.get(t, "/api/v1/integrations/ovhsms/dlr/"+candidate+"?id=424242&ptt=4")
		require.Equal(t, http.StatusNotFound, rec.Code)
	}
}

// TestReplayIsIdempotent proves OVH's retries are harmless: the same callback
// delivered five times leaves exactly the same stored state.
func TestReplayIsIdempotent(t *testing.T) {
	t.Parallel()

	env := setupDLR(t, testToken)

	var first string

	for i := range 5 {
		rec := env.get(t, "/api/v1/integrations/ovhsms/dlr/"+testToken+"?id=424242&ptt=4")
		require.Equal(t, http.StatusNoContent, rec.Code)

		body := env.deliveryBody(t)
		if i == 0 {
			first = body
		}

		require.Equal(t, first, body, "a replayed callback must not change the stored state")
	}

	require.NotEmpty(t, first)
}

// TestMalformedPayloadIsAcceptedAndInert proves every payload field is treated
// as untrusted: a missing id, a garbage ptt, an oversized value and an
// injection attempt must all be handled without a 5xx and without echoing the
// input back into stored state.
func TestMalformedPayloadIsAcceptedAndInert(t *testing.T) {
	t.Parallel()

	env := setupDLR(t, testToken)

	longID := ""
	for range 500 {
		longID += "9"
	}

	cases := []string{
		"",                                     // no query at all
		"?ptt=4",                               // no id
		"?id=424242&ptt=not-a-number",          // unparseable status
		"?id=424242&ptt=%3Cscript%3E",          // injection attempt in the status
		"?id=" + longID + "&ptt=4",             // oversized id
		"?id=424242&ptt=4&description=%3Cb%3E", // unused field
	}

	for _, q := range cases {
		rec := env.get(t, "/api/v1/integrations/ovhsms/dlr/"+testToken+q)
		require.Equal(t, http.StatusNoContent, rec.Code, "query %q must not 5xx", q)
	}

	// Nothing an attacker supplied ever reaches stored state verbatim.
	body := env.deliveryBody(t)
	require.NotContains(t, body, "<script>")
	require.NotContains(t, body, "<b>")
	require.NotContains(t, body, longID)
}

// TestPttMapping walks the status codes the timeline renders.
func TestPttMapping(t *testing.T) {
	t.Parallel()

	tests := []struct{ ptt, want string }{
		{"4", "delivered"},
		{"3", "pending"},
		{"23", "failed"},
		{"6", "unknown"},
	}

	for _, tt := range tests {
		t.Run("ptt="+tt.ptt, func(t *testing.T) {
			t.Parallel()

			env := setupDLR(t, testToken)
			rec := env.get(t, "/api/v1/integrations/ovhsms/dlr/"+testToken+"?id=424242&ptt="+tt.ptt)
			require.Equal(t, http.StatusNoContent, rec.Code)
			require.Contains(t, env.deliveryBody(t), tt.want)
		})
	}
}

// TestResolveToken pins the two ways a token comes to exist, and the case
// where it must not: no explicit token and no JWT secret disables the endpoint
// rather than serving a guessable URL.
func TestResolveToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	explicit := &config.Config{}
	explicit.SMS.OVH.DLRToken = "  explicit-token  "
	explicit.Auth.JWTSecret = "jwt"
	r.Equal("explicit-token", ResolveToken(explicit))

	derived := &config.Config{}
	derived.Auth.JWTSecret = "jwt-secret"
	first := ResolveToken(derived)
	r.Len(first, 64, "a derived token must be a full SHA-256 hex digest")
	r.Equal(first, ResolveToken(derived), "derivation must be deterministic")

	rotated := &config.Config{}
	rotated.Auth.JWTSecret = "jwt-secret-2"
	r.NotEqual(first, ResolveToken(rotated), "rotating the JWT secret must rotate the URL")

	r.Empty(ResolveToken(&config.Config{}))
}

func TestCallbackURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal("https://app.example.com/api/v1/integrations/ovhsms/dlr/tok",
		CallbackURL("https://app.example.com/", "tok"))
	r.Empty(CallbackURL("", "tok"))
	r.Empty(CallbackURL("https://app.example.com", ""))
}
