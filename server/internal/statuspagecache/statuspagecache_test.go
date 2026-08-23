package statuspagecache_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/statuspagecache"
)

// TestControlIsPublicOnlyForPublicPages pins the whole rule, including the
// case nobody writes on purpose: an unrecognized visibility. The helper
// allowlists `public` rather than denylisting the two gated values, so a
// fourth visibility added later arrives locked instead of world-cacheable.
func TestControlIsPublicOnlyForPublicPages(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		visibility string
		want       string
	}{
		{models.StatusPageVisibilityPublic, "public, max-age=60"},
		{models.StatusPageVisibilityPassword, "private, no-store"},
		{models.StatusPageVisibilityPrivate, "private, no-store"},
		{"", "private, no-store"},
		{"some-future-mode", "private, no-store"},
		{"Public", "private, no-store"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.visibility, func(t *testing.T) {
			t.Parallel()

			got := statuspagecache.Control(testCase.visibility, statuspagecache.PageMaxAge)
			require.Equal(t, testCase.want, got)

			if testCase.want != "public, max-age=60" {
				require.NotContains(t, got, "public")
			}
		})
	}
}

// TestMaxAgeIsRenderedInSeconds covers the feed's longer budget traveling
// through the same helper, so a second surface cannot end up with a directive
// the first one never sees.
func TestMaxAgeIsRenderedInSeconds(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("public, max-age=300",
		statuspagecache.Control(models.StatusPageVisibilityPublic, statuspagecache.FeedMaxAge))
	r.Equal("public, max-age=90",
		statuspagecache.Control(models.StatusPageVisibilityPublic, 90*time.Second))

	// A gated page ignores the budget entirely — there is no "cache it for a
	// shorter while" answer for a page you need a password to read.
	r.Equal("private, no-store",
		statuspagecache.Control(models.StatusPageVisibilityPassword, statuspagecache.FeedMaxAge))
}

// TestApplySetsBothHeaders pins that every surface routed through the helper
// gets Vary alongside Cache-Control — and that the two branches list DIFFERENT
// headers. `Cookie` must not appear on a public response: Cloudflare, Fastly
// and Varnish all decline to cache one that carries it, which would quietly
// undo the shared-cache win this whole change exists to buy.
func TestApplySetsBothHeaders(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	public := http.Header{}
	statuspagecache.Apply(public, models.StatusPageVisibilityPublic, statuspagecache.PageMaxAge)
	r.Equal("public, max-age=60", public.Get("Cache-Control"))
	r.Equal("X-Forwarded-Proto", public.Get("Vary"))
	r.NotContains(public.Get("Vary"), "Cookie")

	// Apply on a gated page must land on exactly the ApplyGated answer, so the
	// two entry points cannot drift.
	viaApply := http.Header{}
	statuspagecache.Apply(viaApply, models.StatusPageVisibilityPassword, statuspagecache.PageMaxAge)

	gated := http.Header{}
	statuspagecache.ApplyGated(gated)

	r.Equal("private, no-store", gated.Get("Cache-Control"))
	r.Equal("Cookie, X-Forwarded-Proto", gated.Get("Vary"))
	r.Equal(gated, viaApply)
}
