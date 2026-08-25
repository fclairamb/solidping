package checks_test

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// slugLadderCheckCount is deliberately past the readable "-2".."-99" ladder.
// 99 checks share the base slug before the 100th asks for one, so every
// creation from #100 on is served by the random-discriminator fallback.
const slugLadderCheckCount = 120

// TestAutoSlugSurvivesNumericLadderExhaustion pins the negative that used to be
// a 500.
//
// An auto-generated check slug derives from the check's TARGET, not its name:
// checkhttp's Validate sets `spec.Slug = "http-" + hostname`, so every check
// pointed at one host wants the same base slug. ensureUniqueSlug walked
// "-2".."-99" and then gave up with ErrSlugGenerationFailed, which the create
// handler could only render as `500 {"code":"INTERNAL_ERROR","detail":"could
// not generate unique slug after 99 attempts"}`.
//
// Monitoring 100+ URLs on one domain is an ordinary thing to do — and it is
// also what the dash0 E2E suite does cumulatively against the shared `test`
// org, which is how this surfaced. The 100th creation must succeed, and every
// slug must still be distinct.
func TestAutoSlugSurvivesNumericLadderExhaustion(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	r.NoError(dbSvc.Initialize(ctx))

	svc, org := newSlugRaceService(t, dbSvc, "ladder")

	seen := make(map[string]int, slugLadderCheckCount)
	for i := range slugLadderCheckCount {
		resp, createErr := createHTTPCheck(ctx, svc, org.Slug, "probe "+strconv.Itoa(i), "")
		r.NoErrorf(createErr, "creation #%d (1-based #%d) must succeed", i, i+1)
		r.NotNil(resp.Slug)

		slug := *resp.Slug
		r.NotEmpty(slug)

		if prev, dup := seen[slug]; dup {
			r.Failf("duplicate slug", "creation #%d reused slug %q from creation #%d", i, slug, prev)
		}
		seen[slug] = i
	}

	r.Len(seen, slugLadderCheckCount)

	// Positive control on the ladder itself: the readable suffixes are still
	// what the first hundred get, so the fallback did not quietly replace them.
	r.Contains(seen, "http-race-example-com")
	r.Contains(seen, "http-race-example-com-2")
	r.Contains(seen, "http-race-example-com-99")

	// Every slug must remain a legal slug, random discriminator included.
	for slug := range seen {
		r.Regexp(`^[a-z][a-z0-9-]{2,99}$`, slug)
		r.LessOrEqual(len(slug), 50)
	}
}
