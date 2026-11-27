package middleware

import (
	"net/http"

	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/statuspagekiosk"
)

// StatusPageMemberGrant lets the signed-in members of an organization read its
// `private` and `password` status pages on the public routes (spec
// 2026-11-27-01).
//
// It soft-authenticates the request (SoftAuth: a missing or invalid credential
// continues as anonymous, never 401), then, when a user is on the context and
// is granted access to the `:org` of the URL by the same rule as every org
// route (auth.AuthorizeOrgAccess: token scoped to the org, then membership),
// records the grant for statuspagekiosk.Decide. Every role reads status pages
// today, so membership is the whole check.
//
// Anything that fails here is silent: the gate then answers exactly what it
// answers an anonymous caller, so the public routes do not become an oracle
// for "is this session valid" or "is this user a member".
func (m *AuthMiddleware) StatusPageMemberGrant(next httpx.HandlerFunc) httpx.HandlerFunc {
	grant := func(writer http.ResponseWriter, req *http.Request) error {
		user, userOK := GetUserFromContext(req.Context())
		claims, claimsOK := GetClaimsFromContext(req.Context())
		orgSlug := httpx.Param(req, "org")

		if !userOK || !claimsOK || user == nil || claims == nil || orgSlug == "" {
			return next(writer, req)
		}

		org, denial := auth.AuthorizeOrgAccess(req.Context(), m.dbService, claims, user, orgSlug)
		if denial != auth.OrgAccessGranted || org == nil {
			return next(writer, req)
		}

		return next(writer, req.WithContext(statuspagekiosk.WithMemberOrg(req.Context(), org.UID)))
	}

	soft := m.SoftAuth(grant)

	return func(writer http.ResponseWriter, req *http.Request) error {
		// A custom domain never carries the dashboard session: skip the
		// authentication altogether there.
		if statuspagekiosk.MemberAccessDisabled(req.Context()) {
			return next(writer, req)
		}

		return soft(writer, req)
	}
}
