package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Per-user UI state (spec 2026-08-28-17): the getting-started checklist
// stores its dismissal server-side so it survives a move to another device,
// which is exactly what the old localStorage banner could not do. These tests
// pin the three things that make the endpoint safe to ship: the key
// allowlist, the value size cap, and — the important one — that one user
// cannot read or clobber another's entry.

const uiStateOnboardingPath = "/api/v1/me/ui-state/onboarding." + TestOrgSlug

// uiStateRequest issues an authenticated request against the ui-state API
// with the given bearer token and returns the status and decoded body.
func uiStateRequest(
	t *testing.T, ts *TestServer, method, path, token, body string,
) (int, map[string]any) {
	t.Helper()

	var reader *bytes.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	} else {
		reader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, ts.HTTPServer.URL+path, reader)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	var out map[string]any

	_ = json.NewDecoder(resp.Body).Decode(&out)

	return resp.StatusCode, out
}

// seedSecondUser creates a second user with their own PAT so the isolation
// test speaks over the wire as a genuinely different principal.
func seedSecondUser(t *testing.T, ts *TestServer) string {
	t.Helper()

	ctx := t.Context()
	dbSvc := ts.Server.DBService()
	now := time.Now()

	user := models.NewUser("second-uistate@acme.com")
	require.NoError(t, dbSvc.CreateUser(ctx, user))

	orgUID := "10000000-0000-0000-0000-000000000001"
	member := models.NewOrganizationMember(orgUID, user.UID, models.MemberRoleAdmin)
	member.JoinedAt = &now
	require.NoError(t, dbSvc.CreateOrganizationMember(ctx, member))

	token := &models.UserToken{
		UID:             "20000000-0000-0000-0000-000000000003",
		UserUID:         user.UID,
		OrganizationUID: &orgUID,
		Token:           "pat_test_second",
		Type:            models.TokenTypePAT,
		Properties:      models.JSONMap{"name": "Second PAT"},
		CreatedAt:       now, UpdatedAt: now,
	}
	require.NoError(t, dbSvc.CreateUserToken(ctx, token))

	return "pat_test_second"
}

func TestUIStateRoundTrip(t *testing.T) {
	t.Parallel()

	ts := NewTestServer(t)
	r := require.New(t)

	// Nothing stored yet.
	status, _ := uiStateRequest(t, ts, http.MethodGet, uiStateOnboardingPath, "pat_test", "")
	r.Equal(http.StatusNotFound, status)

	status, body := uiStateRequest(t, ts, http.MethodPut, uiStateOnboardingPath, "pat_test",
		`{"dismissedAt":"2026-08-28T10:00:00Z"}`)
	r.Equal(http.StatusOK, status)
	r.Equal("2026-08-28T10:00:00Z", body["value"].(map[string]any)["dismissedAt"])

	status, body = uiStateRequest(t, ts, http.MethodGet, uiStateOnboardingPath, "pat_test", "")
	r.Equal(http.StatusOK, status)
	r.Equal("2026-08-28T10:00:00Z", body["value"].(map[string]any)["dismissedAt"])

	status, _ = uiStateRequest(t, ts, http.MethodDelete, uiStateOnboardingPath, "pat_test", "")
	r.Equal(http.StatusNoContent, status)

	status, _ = uiStateRequest(t, ts, http.MethodGet, uiStateOnboardingPath, "pat_test", "")
	r.Equal(http.StatusNotFound, status, "the entry is gone after a delete")

	// The re-enable control in the account area deletes an entry that may
	// already be absent; that must not error.
	status, _ = uiStateRequest(t, ts, http.MethodDelete, uiStateOnboardingPath, "pat_test", "")
	r.Equal(http.StatusNoContent, status)
}

// The stored key names the organization's UID, so a value written under the
// slug is still found after the org is renamed.
func TestUIStateKeyIsResolvedToOrgUID(t *testing.T) {
	t.Parallel()

	ts := NewTestServer(t)
	r := require.New(t)

	status, _ := uiStateRequest(t, ts, http.MethodPut, uiStateOnboardingPath, "pat_test", `{"dismissedAt":"x"}`)
	r.Equal(http.StatusOK, status)

	// The same entry is addressable by UID.
	byUID := "/api/v1/me/ui-state/onboarding.10000000-0000-0000-0000-000000000001"
	status, body := uiStateRequest(t, ts, http.MethodGet, byUID, "pat_test", "")
	r.Equal(http.StatusOK, status)
	r.Equal("x", body["value"].(map[string]any)["dismissedAt"])
}

func TestUIStateRejectsKeysOutsideTheAllowlist(t *testing.T) {
	t.Parallel()

	ts := NewTestServer(t)
	r := require.New(t)

	// Positive control: the allowed key is accepted, so the rejections below
	// are about the key and not about the request plumbing.
	status, _ := uiStateRequest(t, ts, http.MethodPut, uiStateOnboardingPath, "pat_test", `{"a":1}`)
	r.Equal(http.StatusOK, status)

	for _, key := range []string{"theme", "theme.test-org", "onboarding", "onboarding.a_b"} {
		path := "/api/v1/me/ui-state/" + key

		status, body := uiStateRequest(t, ts, http.MethodPut, path, "pat_test", `{"a":1}`)
		r.Equal(http.StatusBadRequest, status, "key %q must be rejected", key)
		r.Equal("VALIDATION_ERROR", body["code"], "key %q", key)

		status, _ = uiStateRequest(t, ts, http.MethodGet, path, "pat_test", "")
		r.Equal(http.StatusBadRequest, status, "key %q must be rejected on read too", key)
	}

	// Well-shaped but naming an organization that does not exist.
	status, body := uiStateRequest(t, ts,
		http.MethodPut, "/api/v1/me/ui-state/onboarding.nope-not-here", "pat_test", `{"a":1}`)
	r.Equal(http.StatusNotFound, status)
	r.Equal("ORGANIZATION_NOT_FOUND", body["code"])
}

func TestUIStateRejectsOversizedValues(t *testing.T) {
	t.Parallel()

	ts := NewTestServer(t)
	r := require.New(t)

	// Positive control: a payload just under the cap is stored.
	underCap := `{"blob":"` + strings.Repeat("a", 4000) + `"}`
	r.Less(len(underCap), 4096)

	status, _ := uiStateRequest(t, ts, http.MethodPut, uiStateOnboardingPath, "pat_test", underCap)
	r.Equal(http.StatusOK, status)

	overCap := `{"blob":"` + strings.Repeat("a", 8000) + `"}`

	status, body := uiStateRequest(t, ts, http.MethodPut, uiStateOnboardingPath, "pat_test", overCap)
	r.Equal(http.StatusBadRequest, status)
	r.Equal("VALIDATION_ERROR", body["code"])

	// The oversized write must not have replaced the value that was there.
	status, body = uiStateRequest(t, ts, http.MethodGet, uiStateOnboardingPath, "pat_test", "")
	r.Equal(http.StatusOK, status)
	r.Len(body["value"].(map[string]any)["blob"], 4000)
}

func TestUIStateIsIsolatedPerUser(t *testing.T) {
	t.Parallel()

	ts := NewTestServer(t)
	r := require.New(t)

	secondToken := seedSecondUser(t, ts)

	status, _ := uiStateRequest(t, ts, http.MethodPut, uiStateOnboardingPath, "pat_test", `{"owner":"first"}`)
	r.Equal(http.StatusOK, status)

	// Positive control: the first user reads their own value back.
	status, body := uiStateRequest(t, ts, http.MethodGet, uiStateOnboardingPath, "pat_test", "")
	r.Equal(http.StatusOK, status)
	r.Equal("first", body["value"].(map[string]any)["owner"])

	// The second user — a member of the same org, so the key resolves
	// identically — sees nothing.
	status, _ = uiStateRequest(t, ts, http.MethodGet, uiStateOnboardingPath, secondToken, "")
	r.Equal(http.StatusNotFound, status, "one user must not read another's ui-state")

	// And their write does not clobber the first user's value.
	status, _ = uiStateRequest(t, ts, http.MethodPut, uiStateOnboardingPath, secondToken, `{"owner":"second"}`)
	r.Equal(http.StatusOK, status)

	status, body = uiStateRequest(t, ts, http.MethodGet, uiStateOnboardingPath, "pat_test", "")
	r.Equal(http.StatusOK, status)
	r.Equal("first", body["value"].(map[string]any)["owner"])

	// Nor does their delete.
	status, _ = uiStateRequest(t, ts, http.MethodDelete, uiStateOnboardingPath, secondToken, "")
	r.Equal(http.StatusNoContent, status)

	status, body = uiStateRequest(t, ts, http.MethodGet, uiStateOnboardingPath, "pat_test", "")
	r.Equal(http.StatusOK, status)
	r.Equal("first", body["value"].(map[string]any)["owner"])
}

func TestUIStateRequiresAuthentication(t *testing.T) {
	t.Parallel()

	ts := NewTestServer(t)
	r := require.New(t)

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, ts.HTTPServer.URL+uiStateOnboardingPath, nil)
	r.NoError(err)

	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)

	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusUnauthorized, resp.StatusCode)
}
