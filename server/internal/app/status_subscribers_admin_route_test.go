package app

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// TestStatusPageSubscriberListIsAdminOnly pins spec 2026-11-02-04 through the
// real router: the subscriber list carries third parties' email addresses, so
// a viewer and a `user` member are refused with 403 FORBIDDEN, while an admin
// and an owner get the addresses. DELETE keeps its write floor (`user` and up).
func TestStatusPageSubscriberListIsAdminOnly(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	env := newMembersRouteEnv(t)

	page := models.NewStatusPage(env.orgUID, "Subscribers Admin", "subscribers-admin")
	r.NoError(env.server.dbService.CreateStatusPage(ctx, page))

	newSub := func(email string) *models.StatusPageSubscriber {
		sub := models.NewStatusPageSubscriber(env.orgUID, page.UID, email, models.SubscriberScopePage)
		sub.ConfirmToken = uuid.NewString()
		sub.UnsubscribeToken = uuid.NewString()
		r.NoError(env.server.dbService.CreateSubscriber(ctx, sub))

		return sub
	}

	first := newSub("first-subscriber@acme.example")
	second := newSub("second-subscriber@acme.example")

	listPath := "/api/v1/orgs/" + env.orgSlug + "/status-pages/" + page.UID + "/subscribers"

	for _, role := range []models.MemberRole{models.MemberRoleViewer, models.MemberRoleUser} {
		resp := env.do(http.MethodGet, listPath, env.jwts[role], "", nil)
		r.Equal(http.StatusForbidden, resp.StatusCode, "list must be refused for role %s", role)
		r.Equal(base.ErrorCodeForbidden, decodeErrorCode(t, resp))
		_ = resp.Body.Close()
	}

	for _, role := range []models.MemberRole{models.MemberRoleAdmin, models.MemberRoleOwner} {
		resp := env.do(http.MethodGet, listPath, env.jwts[role], "", nil)
		r.Equal(http.StatusOK, resp.StatusCode, "list must be open to role %s", role)

		var payload struct {
			Data []struct {
				Email string `json:"email"`
			} `json:"data"`
		}
		r.NoError(json.NewDecoder(resp.Body).Decode(&payload))
		_ = resp.Body.Close()

		emails := make([]string, 0, len(payload.Data))
		for _, s := range payload.Data {
			emails = append(emails, s.Email)
		}
		r.ElementsMatch([]string{"first-subscriber@acme.example", "second-subscriber@acme.example"}, emails)
	}

	// DELETE keeps its current rule: a viewer is refused, a `user` may remove.
	viewerDel := env.do(http.MethodDelete, listPath+"/"+first.UID, env.jwts[models.MemberRoleViewer], "", nil)
	r.Equal(http.StatusForbidden, viewerDel.StatusCode)
	_ = viewerDel.Body.Close()

	userDel := env.do(http.MethodDelete, listPath+"/"+second.UID, env.jwts[models.MemberRoleUser], "", nil)
	r.Less(userDel.StatusCode, http.StatusMultipleChoices, "a user member keeps DELETE")
	_ = userDel.Body.Close()
}
