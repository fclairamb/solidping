package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// validateDocumentBody is a minimal, perfectly valid config-as-code document.
// Valid on purpose: these tests are about WHO may ask, so a rejection must
// never be explainable by the document itself.
const validateDocumentBody = `version: 2
organization: viewerguard
secrets: stripped
checks:
  - name: Authz
    slug: authz
    type: http
    config:
      url: https://acme.com/authz
`

// postValidate issues one POST to /checks/validate and returns the status and
// the raw body.
func postValidate(t *testing.T, env *viewerEnv, token, query, contentType, body string) (int, []byte) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		env.ts.URL+"/api/v1/orgs/"+env.org.Slug+"/checks/validate"+query, bytes.NewReader([]byte(body)))
	require.NoError(t, err)

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := env.ts.Client().Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	raw := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		raw = append(raw, buf[:n]...)
		if readErr != nil {
			break
		}
	}

	return resp.StatusCode, raw
}

// adminToken adds an admin member to the viewer fixture and mints a token for
// them. The fixture ships a viewer and an ordinary user only, because those
// are the two roles the write floor is about; `?plan=true` needs the third.
func adminToken(t *testing.T, env *viewerEnv) string {
	t.Helper()
	r := require.New(t)
	ctx := context.Background()

	user := models.NewUser("admin@viewerguard.example")
	r.NoError(env.server.dbService.CreateUser(ctx, user))

	now := time.Now()
	member := models.NewOrganizationMember(env.org.UID, user.UID, models.MemberRoleAdmin)
	member.JoinedAt = &now
	r.NoError(env.server.dbService.CreateOrganizationMember(ctx, member))

	return mintTestToken(t, env.server, user.UID, env.org.Slug, string(models.MemberRoleAdmin), false)
}

// TestValidateDocumentIsReadableByAViewer is the authorization half of spec
// 2026-09-11-04's first proposal, driven through the REAL route table.
//
// The point of the change is that a CI job asking "is this file valid?" should
// not need a write-capable token. Before it, the only whole-document validator
// was reachable through /import and /apply, both admin-only — which is the
// structural reason third parties kept their own re-implementations, and why
// those drifted from the server the moment a check type was added.
//
// The three properties, all on the same route:
//
//  1. A viewer may validate a DOCUMENT. It writes nothing.
//  2. A viewer may NOT validate a single check — that path keeps the write
//     floor it has always had, and answers with the floor's own message.
//  3. `?plan=true` is admin-only in either case: the plan reads the whole
//     organization's check set.
func TestValidateDocumentIsReadableByAViewer(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newViewerEnv(t)

	status, body := postValidate(t, env, env.viewerToken, "", "application/yaml", validateDocumentBody)
	r.Equalf(http.StatusOK, status, "a viewer must be able to validate a document: %s", body)

	var resp struct {
		Valid  bool             `json:"valid"`
		Issues []map[string]any `json:"issues"`
		Plan   any              `json:"plan"`
	}
	r.NoError(json.Unmarshal(body, &resp))
	r.True(resp.Valid, "the fixture document is valid: %s", body)
	r.Nil(resp.Plan, "no plan was asked for")
}

// TestValidateSingleCheckStillRefusesAViewer is the boundary that must NOT
// have moved with it: the same route, a non-document body, and the write floor
// answering exactly as before. It is asserted on the floor's MESSAGE, because
// an unrelated 403 must not be able to masquerade as the gate working.
func TestValidateSingleCheckStillRefusesAViewer(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newViewerEnv(t)

	status, body := postValidate(t, env, env.viewerToken, "", "application/json",
		`{"type":"http","config":{"url":"https://acme.com/single"}}`)
	r.Equal(http.StatusForbidden, status, string(body))

	var errBody struct {
		Title string `json:"title"`
	}
	r.NoError(json.Unmarshal(body, &errBody))
	r.Equal(middleware.ViewerWriteMessage, errBody.Title)

	// Positive control: an ordinary member is NOT refused — a gate that also
	// locked out `user` would be worse than the hole it closes.
	status, body = postValidate(t, env, env.userToken, "", "application/json",
		`{"type":"http","config":{"url":"https://acme.com/single"}}`)
	r.Equalf(http.StatusOK, status, "an ordinary member must still validate a single check: %s", body)
}

// TestValidateDocumentPlanRequiresAdmin covers the third floor. The plan is a
// read of the organization's whole check set, and it is the only part of this
// endpoint that is.
func TestValidateDocumentPlanRequiresAdmin(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newViewerEnv(t)

	for _, token := range []string{env.viewerToken, env.userToken} {
		status, body := postValidate(t, env, token, "?plan=true", "application/yaml", validateDocumentBody)
		r.Equalf(http.StatusForbidden, status, "?plan=true must need admin: %s", body)
	}

	status, body := postValidate(t, env, adminToken(t, env), "?plan=true",
		"application/yaml", validateDocumentBody)
	r.Equalf(http.StatusOK, status, "an admin must get the plan: %s", body)

	var resp struct {
		Valid bool `json:"valid"`
		Plan  *struct {
			DryRun  bool `json:"dryRun"`
			Created int  `json:"created"`
		} `json:"plan"`
	}
	r.NoError(json.Unmarshal(body, &resp))
	r.NotNil(resp.Plan, "an admin's ?plan=true must carry a plan: %s", body)
	r.True(resp.Plan.DryRun, "the plan must never be a write")
	r.Equal(1, resp.Plan.Created, "the fixture org holds no such check yet")
}

// TestImportAndApplyStayAdminOnly is the guard against the collateral this
// change could easily have caused. Relaxing /checks/validate to member level
// must not have relaxed its neighbours: they mutate, and apply can delete by
// absence.
func TestImportAndApplyStayAdminOnly(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newViewerEnv(t)

	for _, path := range []string{"/import", "/apply", "/export"} {
		for _, token := range []string{env.viewerToken, env.userToken} {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
				env.ts.URL+"/api/v1/orgs/"+env.org.Slug+"/checks"+path,
				bytes.NewReader([]byte(validateDocumentBody)))
			r.NoError(err)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/yaml")

			resp, err := env.ts.Client().Do(req)
			r.NoError(err)
			_ = resp.Body.Close()

			r.NotEqualf(http.StatusOK, resp.StatusCode,
				"%s must not be reachable below admin", path)
		}
	}
}
