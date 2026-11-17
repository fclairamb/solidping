package app

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// TestEmailSuppressionsAreAdminOnly pins spec 2026-11-17-07 through the real
// router: the suppression list carries recipients' email addresses (mostly
// third parties) and DELETE overrides their own opt-out, so a viewer and a
// `user` member get 403 FORBIDDEN on both, while an admin lists and deletes.
func TestEmailSuppressionsAreAdminOnly(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	env := newMembersRouteEnv(t)

	first := models.NewEmailSuppression(env.orgUID, "first-unsub@acme.example", nil, "link")
	r.NoError(env.server.dbService.CreateEmailSuppression(ctx, first))
	second := models.NewEmailSuppression(env.orgUID, "second-unsub@acme.example", nil, "header")
	r.NoError(env.server.dbService.CreateEmailSuppression(ctx, second))

	listPath := "/api/v1/orgs/" + env.orgSlug + "/email-suppressions"

	for _, role := range []models.MemberRole{models.MemberRoleViewer, models.MemberRoleUser} {
		resp := env.do(http.MethodGet, listPath, env.jwts[role], "", nil)
		r.Equal(http.StatusForbidden, resp.StatusCode, "list must be refused for role %s", role)
		r.Equal(base.ErrorCodeForbidden, decodeErrorCode(t, resp))
		_ = resp.Body.Close()

		del := env.do(http.MethodDelete, listPath+"/"+first.UID, env.jwts[role], "", nil)
		r.Equal(http.StatusForbidden, del.StatusCode, "delete must be refused for role %s", role)
		r.Equal(base.ErrorCodeForbidden, decodeErrorCode(t, del))
		_ = del.Body.Close()
	}

	listEmails := func(role models.MemberRole) []string {
		resp := env.do(http.MethodGet, listPath, env.jwts[role], "", nil)
		defer func() { _ = resp.Body.Close() }()
		r.Equal(http.StatusOK, resp.StatusCode, "list must be open to role %s", role)

		var payload struct {
			Data []struct {
				Email string `json:"email"`
			} `json:"data"`
		}
		r.NoError(json.NewDecoder(resp.Body).Decode(&payload))

		emails := make([]string, 0, len(payload.Data))
		for _, s := range payload.Data {
			emails = append(emails, s.Email)
		}

		return emails
	}

	// The refused deletes above must not have removed anything.
	r.ElementsMatch([]string{"first-unsub@acme.example", "second-unsub@acme.example"},
		listEmails(models.MemberRoleAdmin))
	r.ElementsMatch([]string{"first-unsub@acme.example", "second-unsub@acme.example"},
		listEmails(models.MemberRoleOwner))

	del := env.do(http.MethodDelete, listPath+"/"+first.UID, env.jwts[models.MemberRoleAdmin], "", nil)
	r.Equal(http.StatusNoContent, del.StatusCode)
	_ = del.Body.Close()

	r.ElementsMatch([]string{"second-unsub@acme.example"}, listEmails(models.MemberRoleAdmin))
}
