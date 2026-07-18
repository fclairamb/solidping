package auth

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestTokenToInfoIsCurrent verifies isCurrent is flagged on the caller's own
// credential row across both session (refresh) and OAuth (oauth_refresh) grants,
// and never on a PAT — the extension that lets an MCP client / CLI identify the
// grant it rides on in GET /tokens.
func TestTokenToInfoIsCurrent(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	orgUID := "org-uid"

	grant := models.NewUserToken("user-uid", &orgUID, "grant-token", models.TokenTypeOAuthRefresh)
	r.True(tokenToInfo(grant, "slug", grant.UID).IsCurrent,
		"an oauth_refresh grant matching the caller RefreshUID must be isCurrent")
	r.False(tokenToInfo(grant, "slug", "other-uid").IsCurrent,
		"a grant not matching the caller RefreshUID must not be isCurrent")
	r.False(tokenToInfo(grant, "slug", "").IsCurrent,
		"a listing without a RefreshUID (e.g. PAT-authenticated caller) flags nothing")

	sess := models.NewUserToken("user-uid", &orgUID, "sess-token", models.TokenTypeRefresh)
	r.True(tokenToInfo(sess, "slug", sess.UID).IsCurrent,
		"a session refresh row still flags isCurrent (unchanged behavior)")

	pat := models.NewUserToken("user-uid", &orgUID, "pat-token", models.TokenTypePAT)
	r.False(tokenToInfo(pat, "slug", pat.UID).IsCurrent,
		"a PAT must never be flagged isCurrent")
}
