package app

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Spec 2026-11-19-02: through the real router, a PAT and an impersonation
// token cannot add, re-enable or confirm a paging contact, and get the
// explicit 403 code. Reading the routes and removing a contact still pass, and
// the member's own session still adds a contact.

// contactBindingCalls are the four refused calls, on org slug.
func contactBindingCalls(slug string) []struct{ name, method, path, body string } {
	prefix := "/api/v1/orgs/" + slug + "/users/me"

	return []struct{ name, method, path, body string }{
		{"add contact", http.MethodPost, prefix + "/notification-contacts", `{"type":"ntfy_topic","value":"mallory"}`},
		{"enable route", http.MethodPatch, prefix + "/notification-routes/r1", `{"enabled":true}`},
		{"verify", http.MethodPost, prefix + "/notification-contacts/c1/verify", `{}`},
		{"verify confirm", http.MethodPost, prefix + "/notification-contacts/c1/verify/confirm", `{"code":"123456"}`},
	}
}

// firstContactUID lists the caller's routes (which seeds the default email
// contact) and returns the first contact's uid.
func firstContactUID(t *testing.T, env *impersonationEnv, slug, token string) string {
	t.Helper()
	r := require.New(t)

	status, body := env.do(http.MethodGet, "/api/v1/orgs/"+slug+"/users/me/notification-routes", token, "")
	r.Equal(http.StatusOK, status, body)

	data, ok := body["data"].([]any)
	r.True(ok, body)
	r.NotEmpty(data)

	route, ok := data[0].(map[string]any)
	r.True(ok)
	contact, ok := route["contact"].(map[string]any)
	r.True(ok)
	uid, _ := contact["uid"].(string)
	r.NotEmpty(uid)

	return uid
}

//nolint:paralleltest,tparallel // sub-tests share one server
func TestPATCannotBindANotificationContact(t *testing.T) {
	t.Parallel()
	env := newPATBindingEnv(t)

	for _, call := range contactBindingCalls("pata") {
		t.Run(call.name, func(t *testing.T) {
			r := require.New(t)

			status, body := env.do(call.method, call.path, env.pat, call.body)
			r.Equal(http.StatusForbidden, status, body)
			r.Equal(string(base.ErrorCodePATForbidden), body["code"])
		})
	}

	r := require.New(t)

	// Positive controls: the PAT reads the routes and removes a contact.
	uid := firstContactUID(t, env.impersonationEnv, "pata", env.pat)
	status, body := env.do(http.MethodDelete, "/api/v1/orgs/pata/users/me/notification-contacts/"+uid, env.pat, "")
	r.Less(status, 300, body)

	// The member's own session still adds a contact.
	status, body = env.do(http.MethodPost, "/api/v1/orgs/pata/users/me/notification-contacts", env.session,
		`{"type":"ntfy_topic","value":"alice"}`)
	r.Less(status, 300, body)
}

//nolint:paralleltest,tparallel // sub-tests share one server
func TestImpersonationCannotBindANotificationContact(t *testing.T) {
	t.Parallel()
	env := newImpersonationEnv(t, true)
	r := require.New(t)

	status, body := env.do(http.MethodPost, env.impersonatePath(), env.adminToken, `{"orgSlug":"impa"}`)
	r.Equal(http.StatusOK, status, body)
	token, _ := body["accessToken"].(string)
	r.NotEmpty(token)

	for _, call := range contactBindingCalls("impa") {
		t.Run(call.name, func(t *testing.T) {
			r := require.New(t)

			status, body := env.do(call.method, call.path, token, call.body)
			r.Equal(http.StatusForbidden, status, body)
			r.Equal(string(base.ErrorCodeImpersonationForbidden), body["code"])
		})
	}

	// Positive controls: reading the routes and removing a contact pass.
	uid := firstContactUID(t, env, "impa", token)
	status, body = env.do(http.MethodDelete, "/api/v1/orgs/impa/users/me/notification-contacts/"+uid, token, "")
	r.Less(status, 300, body)

	// The member's own session still adds a contact.
	status, body = env.do(http.MethodPost, "/api/v1/orgs/impa/users/me/notification-contacts", env.memberToken,
		`{"type":"ntfy_topic","value":"alice"}`)
	r.Less(status, 300, body)
}
