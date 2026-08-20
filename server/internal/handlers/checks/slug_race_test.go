package checks_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// portSlugRacePG is distinct from every other embedded-Postgres port claimed in
// the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portSlugRacePG = 15484

// raceTarget is ONE url shared by every racing creation, because that is what
// makes them collide: an auto-generated check slug is derived from the check's
// TARGET, not from its name (checkhttp's Validate sets
// `spec.Slug = "http-" + hostname`), so distinct names are irrelevant.
//
// This is exactly the shape of web/dash0/e2e/availability.spec.ts's
// createCheckAndOpen, which fills the same URL into every check it creates and
// only varies the name — the reason that spec was flaky at --workers=2 and
// clean at --workers=1.
const raceTarget = "https://race.example.com/slug"

// concurrentSlugCreations is how many checks the race tests create at once for
// one target.
//
// It is deliberately set WELL ABOVE the insert's stall budget. Losing the slug
// race is not a failure: it means a competing creation committed, so the next
// re-resolve returns a DIFFERENT slug and the loop has made progress. N racers
// make the unluckiest one lose up to N-1 times, and N is set by the caller (a
// scripted import, a burst of dashboard tabs, parallel E2E workers), not by the
// loop — so a budget that charged those legitimate losses would fail here while
// looking perfectly healthy at N=2. Anything at or below the budget could never
// catch that.
const concurrentSlugCreations = 24

// newSlugRaceService builds a checks.Service over the given db.Service with one
// fresh organization, for the concurrent-create contract below.
func newSlugRaceService(
	t *testing.T, dbSvc db.Service, orgSlug string,
) (*checks.Service, *models.Organization) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	org := models.NewOrganization(orgSlug, orgSlug)
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)

	return checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc), org
}

// TestCheckSlugRace_SQLite proves the concurrent-create contract on SQLite.
func TestCheckSlugRace_SQLite(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	r.NoError(dbSvc.Initialize(ctx))

	testCheckSlugRace(t, dbSvc)
}

// TestCheckSlugRace_Postgres is the real-engine twin, and the one that matters
// most here: SQLite runs a single writer (SetMaxOpenConns(1)), so its inserts
// barely overlap, while Postgres runs them for real on a connection pool. A fix
// that only held because SQLite serializes writes would be a production bug —
// and the E2E flake this spec pins was reproduced on Postgres.
func TestCheckSlugRace_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portSlugRacePG,
		RunMode:  "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	// The embedded server runs with max_connections=10 and the embedded
	// constructor leaves the pool unbounded, so the racing goroutines would each
	// open their own connection and be refused with "sorry, too many clients
	// already" long before the server could answer a duplicate key — masking the
	// very contention these tests exist to create. Bounded below 10, the extra
	// goroutines queue on the pool instead.
	dbSvc.DB().SetMaxOpenConns(6)

	testCheckSlugRace(t, dbSvc)
}

// testCheckSlugRace is the engine-agnostic contract, run against both backends.
func testCheckSlugRace(t *testing.T, dbSvc db.Service) {
	t.Helper()

	t.Run("ParallelAutoSlugCreationsAllSucceedWithDistinctSlugs", func(t *testing.T) {
		t.Parallel()
		testParallelAutoSlugCreations(t, dbSvc)
	})

	t.Run("ParallelUserSlugCreationsYieldExactlyOneWinner", func(t *testing.T) {
		t.Parallel()
		testParallelUserSlugCreations(t, dbSvc)
	})

	t.Run("ParallelRenamesToOneSlugYieldExactlyOneWinner", func(t *testing.T) {
		t.Parallel()
		testParallelRenamesToOneSlug(t, dbSvc)
	})

	t.Run("ParallelClonesOfOneCheckAllSucceedWithDistinctSlugs", func(t *testing.T) {
		t.Parallel()
		testParallelClones(t, dbSvc)
	})
}

// createHTTPCheck creates an http check for raceTarget. slug == "" means "let
// the server derive one", which is what the dashboard sends when the user
// leaves the slug field alone — and what makes every caller collide.
func createHTTPCheck(
	ctx context.Context, svc *checks.Service, orgSlug, name, slug string,
) (checks.CheckResponse, error) {
	return svc.CreateCheck(ctx, orgSlug, checks.CreateCheckRequest{
		Name:   name,
		Slug:   slug,
		Type:   "http",
		Config: map[string]any{"url": raceTarget},
	})
}

// testParallelAutoSlugCreations is the race proof, and the direct backend
// analog of two Playwright workers submitting the new-check form at once: N
// goroutines creating checks for the SAME target in the SAME organization must
// ALL succeed, each with its own slug.
//
// Before the fix, the losers of the SELECT-then-INSERT gap surfaced a bare
// unique-index violation on checks_slug_idx, which the create handler could
// only render as a 500 — and in the SPA a rejected createCheck.mutateAsync
// never reaches its navigate() call, which is precisely the
// "page.waitForURL: Timeout 10000ms exceeded" this spec chased.
func testParallelAutoSlugCreations(t *testing.T, dbSvc db.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	svc, org := newSlugRaceService(t, dbSvc, "slug-race-auto")

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		slugs []string
		errs  []error
	)

	start := make(chan struct{})

	for i := 0; i < concurrentSlugCreations; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			<-start

			resp, err := createHTTPCheck(ctx, svc, org.Slug, "Race "+strconv.Itoa(i), "")

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				errs = append(errs, err)

				return
			}

			slugs = append(slugs, derefSlug(resp))
		}()
	}

	close(start)
	wg.Wait()

	r.Empty(errs, "a concurrent create for the same target must never fail")
	r.Len(slugs, concurrentSlugCreations)

	seen := make(map[string]bool, len(slugs))
	for _, slug := range slugs {
		r.NotEmpty(slug, "every check must get a slug")
		r.False(seen[slug], "slug %q was handed out twice", slug)
		seen[slug] = true
	}
}

// testParallelUserSlugCreations is the counterpart contract: an EXPLICIT slug is
// never silently re-resolved into something else. N callers asking for the same
// name means exactly one check is created and every other caller is told the
// slug is taken — the same ErrSlugConflict (400) the pre-insert lookup returns
// when it sees the row, so the answer no longer depends on which side of the
// race the conflict landed on.
func testParallelUserSlugCreations(t *testing.T, dbSvc db.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	svc, org := newSlugRaceService(t, dbSvc, "slug-race-user")

	const wanted = "pinned-slug"

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		created   []string
		conflicts int
		other     []error
	)

	start := make(chan struct{})

	for i := 0; i < concurrentSlugCreations; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			<-start

			resp, err := createHTTPCheck(ctx, svc, org.Slug, "Pinned "+strconv.Itoa(i), wanted)

			mu.Lock()
			defer mu.Unlock()

			switch {
			case err == nil:
				created = append(created, derefSlug(resp))
			case errors.Is(err, checks.ErrSlugConflict):
				conflicts++
			default:
				other = append(other, err)
			}
		}()
	}

	close(start)
	wg.Wait()

	r.Empty(other, "a lost user-slug race must report the conflict, never an opaque driver error")
	r.Len(created, 1, "exactly one caller may claim an explicitly requested slug")
	r.Equal(wanted, created[0], "the winner keeps the slug it asked for, unrenamed")
	r.Equal(concurrentSlugCreations-1, conflicts)
}

// testParallelRenamesToOneSlug covers the third door onto checks_slug_idx.
//
// UpdateCheck's validateAndCheckSlugConflict only proves the slug was free when
// it LOOKED, so a rename carries the same SELECT-then-UPDATE gap as a create.
// N checks renaming to one slug at once must produce exactly one winner and
// N-1 ErrSlugConflicts, whichever side of the gap each conflict landed on —
// never a raw 23505 that the handler can only render as a 500, and never a
// silent rename to something the caller did not ask for (a rename slug is
// always user-provided, so re-resolving it would be the wrong repair).
//
// Losers must also keep the slug they already had: a rejected rename may not
// half-apply.
func testParallelRenamesToOneSlug(t *testing.T, dbSvc db.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	svc, org := newSlugRaceService(t, dbSvc, "slug-race-rename")

	const contested = "contested-rename"

	originals := make([]string, 0, concurrentSlugCreations)
	uids := make([]string, 0, concurrentSlugCreations)

	for i := 0; i < concurrentSlugCreations; i++ {
		slug := "rename-src-" + strconv.Itoa(i)

		resp, err := createHTTPCheck(ctx, svc, org.Slug, "Rename "+strconv.Itoa(i), slug)
		r.NoError(err)

		originals = append(originals, slug)
		uids = append(uids, resp.UID)
	}

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		winners   int
		conflicts int
		other     []error
	)

	start := make(chan struct{})

	for i := 0; i < concurrentSlugCreations; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			<-start

			wanted := contested
			_, err := svc.UpdateCheck(ctx, org.Slug, uids[i], &checks.UpdateCheckRequest{Slug: &wanted})

			mu.Lock()
			defer mu.Unlock()

			switch {
			case err == nil:
				winners++
			case errors.Is(err, checks.ErrSlugConflict):
				conflicts++
			default:
				other = append(other, err)
			}
		}()
	}

	close(start)
	wg.Wait()

	r.Empty(other, "a lost rename race must report the conflict, never an opaque driver error")
	r.Equal(1, winners, "exactly one check may take a contested slug")
	r.Equal(concurrentSlugCreations-1, conflicts)

	// Exactly one check now carries the contested slug, and every loser still
	// carries the slug it started with.
	kept := 0

	for i := 0; i < concurrentSlugCreations; i++ {
		after, err := svc.GetCheck(ctx, org.Slug, uids[i], checks.GetCheckOptions{})
		r.NoError(err)

		if derefSlug(after) == contested {
			kept++

			continue
		}

		r.Equal(originals[i], derefSlug(after), "a rejected rename must not half-apply")
	}

	r.Equal(1, kept)
}

// derefSlug reads a response's slug, which is a *string because a check may
// legitimately have none.
func derefSlug(resp checks.CheckResponse) string {
	if resp.Slug == nil {
		return ""
	}

	return *resp.Slug
}

// testParallelClones covers the second door onto the same insert. A clone's
// default slug is `<source>-copy` — derived from the source, not from anything
// the caller chose — so N clones of one check collide by construction, exactly
// as N creates for one target do. Cloning goes through the same
// insertCheckResolvingSlugRace, and this is the proof that it does: before the
// fix the losers surfaced the raw violation wrapped in "create clone: …".
func testParallelClones(t *testing.T, dbSvc db.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	svc, org := newSlugRaceService(t, dbSvc, "slug-race-clone")

	source, err := createHTTPCheck(ctx, svc, org.Slug, "Clone source", "clone-source")
	r.NoError(err)

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		slugs []string
		errs  []error
	)

	start := make(chan struct{})

	for i := 0; i < concurrentSlugCreations; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			<-start

			resp, cloneErr := svc.CloneCheck(ctx, org.Slug, source.UID, &checks.CloneCheckRequest{})

			mu.Lock()
			defer mu.Unlock()

			if cloneErr != nil {
				errs = append(errs, cloneErr)

				return
			}

			slugs = append(slugs, derefSlug(resp))
		}()
	}

	close(start)
	wg.Wait()

	r.Empty(errs, "a concurrent clone of one check must never fail")
	r.Len(slugs, concurrentSlugCreations)

	seen := make(map[string]bool, len(slugs))
	for _, slug := range slugs {
		r.NotEmpty(slug)
		r.False(seen[slug], "clone slug %q was handed out twice", slug)
		seen[slug] = true
	}
}
