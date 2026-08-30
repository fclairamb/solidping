package statuspagekiosk_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/statuspagekiosk"
	"github.com/fclairamb/solidping/server/internal/statuspagelock"
)

func pageWithToken(visibility, hash string) *models.StatusPage {
	page := &models.StatusPage{
		UID:        "page-uid",
		Visibility: visibility,
		Enabled:    true,
	}

	if hash != "" {
		page.KioskTokenHash = &hash
	}

	if visibility == models.StatusPageVisibilityPassword {
		stored := "argon2id$whatever"
		page.PasswordHash = &stored
	}

	return page
}

func requestCtx(token string) context.Context {
	target := "/api/v1/status-pages/acme/main"
	if token != "" {
		target += "?kiosk=" + token
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)

	return statuspagekiosk.WithGrant(context.Background(), statuspagekiosk.FromRequest(req))
}

// TestGenerateProducesFreshHighEntropyTokens — the token has no rate limiter in
// front of it (a wallboard polls constantly), so entropy is the whole defense.
func TestGenerateProducesFreshHighEntropyTokens(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	seen := make(map[string]bool)

	for range 50 {
		token, hash, err := statuspagekiosk.Generate()
		r.NoError(err)
		r.False(seen[token], "every mint must be unique")
		seen[token] = true

		// 32 bytes of base64url without padding.
		r.GreaterOrEqual(len(token), 42)
		r.Equal(statuspagekiosk.Hash(token), hash)
		r.NotContains(hash, token, "the stored form must not embed the plaintext")
		r.True(statuspagekiosk.Valid(hash, token))
	}
}

// TestValidRejectsEverythingButTheToken. The empty cases matter most: a page
// with no token must not be opened by an empty `?kiosk=`, and a request with no
// parameter must not match an empty stored hash.
func TestValidRejectsEverythingButTheToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	token, hash, err := statuspagekiosk.Generate()
	r.NoError(err)

	r.True(statuspagekiosk.Valid(hash, token), "positive control")
	r.False(statuspagekiosk.Valid("", token), "no stored hash")
	r.False(statuspagekiosk.Valid(hash, ""), "no presented token")
	r.False(statuspagekiosk.Valid("", ""), "neither")
	r.False(statuspagekiosk.Valid(hash, token[:len(token)-1]), "truncated")
	r.False(statuspagekiosk.Valid(hash, token+"x"), "extended")
	r.False(statuspagekiosk.Valid(hash, strings.ToUpper(token)), "case-mangled")
	r.False(statuspagekiosk.Valid(hash, hash), "the hash is not the token")
	r.False(statuspagekiosk.Holds(nil, token), "no page")
	r.False(statuspagekiosk.Holds(pageWithToken(models.StatusPageVisibilityPrivate, ""), token),
		"a page with no token is never held")
}

// TestDecideMatrix is the gate's whole contract in one table. The rows that
// matter are the ones where a WRONG token has to produce EXACTLY the same
// decision as no token — anything else is an existence oracle.
func TestDecideMatrix(t *testing.T) {
	t.Parallel()

	token, hash, err := statuspagekiosk.Generate()
	require.NoError(t, err)

	// Aliases keep the table one row per case; the whole point of the table is
	// that a reader can compare the "no token" and "wrong token" rows at a
	// glance and see that they land on the same decision.
	const (
		pub  = models.StatusPageVisibilityPublic
		priv = models.StatusPageVisibilityPrivate
		pwd  = models.StatusPageVisibilityPassword
	)

	var (
		allow    = statuspagekiosk.DecisionAllow
		notFound = statuspagekiosk.DecisionNotFound
		locked   = statuspagekiosk.DecisionLocked
	)

	testCases := []struct {
		name       string
		visibility string
		storedHash string
		presented  string
		enabled    bool
		unlocked   bool
		want       statuspagekiosk.Decision
	}{
		{"public page, no token", pub, "", "", true, false, allow},
		{"private page, no token", priv, "", "", true, false, notFound},
		{"private page, valid token", priv, hash, token, true, false, allow},
		{"private page, wrong token", priv, hash, "wrong", true, false, notFound},
		{"private page, revoked token", priv, "", token, true, false, notFound},
		{"password page, no token", pwd, "", "", true, false, locked},
		{"password page, valid token", pwd, hash, token, true, false, allow},
		{"password page, wrong token", pwd, hash, "wrong", true, false, locked},
		{"password page, unlock cookie", pwd, "", "", true, true, allow},
		{"disabled public page beats a valid token", pub, hash, token, false, false, notFound},
		{"disabled private page beats a valid token", priv, hash, token, false, false, notFound},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			page := pageWithToken(testCase.visibility, testCase.storedHash)
			page.Enabled = testCase.enabled

			ctx := requestCtx(testCase.presented)
			if testCase.unlocked {
				ctx = statuspagelock.WithGrant(ctx, func(*models.StatusPage) bool { return true })
			}

			require.Equal(t, testCase.want, statuspagekiosk.Decide(ctx, page))
		})
	}
}

// TestNoGrantDeniesByDefault — a caller that never passed through the
// middleware (an MCP tool, a background job, a unit test) presents nothing, and
// nothing must never be read as permission.
func TestNoGrantDeniesByDefault(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	token, hash, err := statuspagekiosk.Generate()
	r.NoError(err)

	page := pageWithToken(models.StatusPageVisibilityPrivate, hash)

	r.False(statuspagekiosk.Allows(context.Background(), page))
	r.Equal(statuspagekiosk.DecisionNotFound, statuspagekiosk.Decide(context.Background(), page))
	r.Equal(statuspagekiosk.DecisionAllow, statuspagekiosk.Decide(requestCtx(token), page),
		"positive control: with the grant mounted it is allowed")
}

// TestWithRequestGrantReadsTheQueryParameter pins the transport: a query
// parameter, because the thing presenting it is a browser opening a URL typed
// into a TV stick and cannot set headers.
func TestWithRequestGrantReadsTheQueryParameter(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	token, hash, err := statuspagekiosk.Generate()
	r.NoError(err)

	page := pageWithToken(models.StatusPageVisibilityPrivate, hash)

	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodGet, "/x?kiosk="+token, nil)
	r.True(statuspagekiosk.Allows(statuspagekiosk.WithRequestGrant(req).Context(), page))

	bare := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", nil)
	r.False(statuspagekiosk.Allows(statuspagekiosk.WithRequestGrant(bare).Context(), page))

	// The token must not be accepted from a header — one transport, one place
	// to audit.
	headered := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", nil)
	headered.Header.Set("X-Kiosk-Token", token)
	r.False(statuspagekiosk.Allows(statuspagekiosk.WithRequestGrant(headered).Context(), page))
}
