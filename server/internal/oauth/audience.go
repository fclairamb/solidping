package oauth

import "github.com/fclairamb/solidping/server/internal/handlers/auth"

// TokenHasResourceAudience reports whether the credential is allowed to act on
// the given resource based on its audience binding (RFC 8707).
//
// Back-compat is the whole point of the empty-audience case: PATs and legacy
// session JWTs carry no `aud` claim and must keep working — they pass the
// audience gate and are governed solely by the existing scope check. An OAuth
// access token, by contrast, is minted with `aud` = the MCP resource, so it is
// accepted here only when its audience list contains that exact resource. This
// stops a token issued for one resource from being replayed at another.
func TokenHasResourceAudience(claims *auth.Claims, resource string) bool {
	if claims == nil {
		return false
	}

	aud := claims.RegisteredClaims.Audience
	if len(aud) == 0 {
		// No audience binding → PAT / legacy session token → back-compat path.
		return true
	}

	for _, a := range aud {
		if a == resource {
			return true
		}
	}

	return false
}
