package signupguard

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPatternCatchesWrappedAndRenamedCalls proves the scan is not fooled by the
// two evasions an earlier single-line, ctx-anchored version missed: a call
// wrapped across lines, and a differently named context variable. A guard that
// can't catch these is worse than no guard, because it reads as coverage it
// does not provide.
func TestPatternCatchesWrappedAndRenamedCalls(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	shouldMatch := []string{
		"if err := s.db.CreateUser(ctx, user); err != nil {",
		"if err := s.db.CreateUser(requestCtx, user); err != nil {",
		"err := s.db.CreateUser(\n\t\tc,\n\t\tuser,\n\t)",
		"return dbSvc.CreateUser(\n\tctx,\n\tuser,\n)",
		"jctx.DBService.CreateUser(ctx, adminUser)",
	}
	for _, src := range shouldMatch {
		r.Truef(createUserCallRe.MatchString(src), "guard must match: %q", src)
	}

	// Sibling APIs that do NOT create an account must never trip the guard.
	shouldNotMatch := []string{
		"s.db.CreateUserProvider(ctx, provider)",
		"s.db.CreateUserToken(ctx, token)",
		"s.db.CreateUserPasskey(ctx, passkey)",
		"// CreateUser is documented here but not called",
	}
	for _, src := range shouldNotMatch {
		r.Falsef(createUserCallRe.MatchString(src), "guard must not match: %q", src)
	}
}
