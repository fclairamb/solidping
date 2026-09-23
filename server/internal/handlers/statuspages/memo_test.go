package statuspages

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/statuspagecache"
	"github.com/fclairamb/solidping/server/internal/statuspagelock"
)

// The counting db.Service decorator itself lives in view_options_test.go (spec
// 2026-09-22-07 introduced it); these are the view-fan-out counters this spec
// needs, on the same type rather than a second one — two decorators over
// db.Service would be two places to keep a signature in sync.
//
// Counting a REAL database rather than faking one: every test below asks "did
// the second read do the WORK again", and a hand-written fake returning
// plausible rows would pin the memo against the fake instead of against the
// query fan-out it exists to avoid.
func (c *countingDB) GetOrganizationBySlug(ctx context.Context, slug string) (*models.Organization, error) {
	c.orgLookups.Add(1)

	return c.Service.GetOrganizationBySlug(ctx, slug)
}

func (c *countingDB) GetStatusPageBySlug(ctx context.Context, orgUID, slug string) (*models.StatusPage, error) {
	c.pageLookups.Add(1)

	return c.Service.GetStatusPageBySlug(ctx, orgUID, slug)
}

func (c *countingDB) ListStatusPageSections(
	ctx context.Context, pageUID string,
) ([]*models.StatusPageSection, error) {
	c.sectionReads.Add(1)

	return c.Service.ListStatusPageSections(ctx, pageUID)
}

func (c *countingDB) ListStatusPageResources(
	ctx context.Context, sectionUID string,
) ([]*models.StatusPageResource, error) {
	c.resourceRead.Add(1)

	return c.Service.ListStatusPageResources(ctx, sectionUID)
}

func (c *countingDB) ListPublicStatusUpdates(
	ctx context.Context, statusPageUID string, historyDays int,
) ([]*db.PublicStatusUpdate, error) {
	c.updateReads.Add(1)

	return c.Service.ListPublicStatusUpdates(ctx, statusPageUID, historyDays)
}

// reset zeroes the view-fan-out counters, so a test can assert on what ONE call
// did.
func (c *countingDB) reset() {
	c.orgLookups.Store(0)
	c.pageLookups.Store(0)
	c.sectionReads.Store(0)
	c.resourceRead.Store(0)
	c.updateReads.Store(0)
}

// computeReads is everything the memo is supposed to save: the section and
// resource fan-out plus the timeline query. Deliberately EXCLUDES the org and
// page lookups, which run on every request by design because the gate reads
// them.
func (c *countingDB) computeReads() int64 {
	return c.sectionReads.Load() + c.resourceRead.Load() + c.updateReads.Load()
}

// memoTestSetup builds a service whose db counts reads, plus an org.
func memoTestSetup(t *testing.T) (context.Context, *Service, *countingDB, *models.Organization) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbService.Initialize(ctx))
	t.Cleanup(func() { _ = dbService.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbService.CreateOrganization(ctx, org))

	counting := &countingDB{Service: dbService}

	return ctx, NewService(counting, &config.Config{}, nil), counting, org
}

// seedMemoPage creates a public page with one section holding one check
// resource, so a view has real work to do.
func seedMemoPage(ctx context.Context, t *testing.T, svc *Service, org *models.Organization) StatusPageResponse {
	t.Helper()

	r := require.New(t)

	page, section := seedPageWithSection(ctx, t, svc, org.Slug, "")

	check := models.NewCheck(org.UID, "api", "http")
	r.NoError(svc.db.CreateCheck(ctx, check))

	_, err := svc.CreateResource(ctx, org.Slug, page.UID, section.UID,
		CreateResourceRequest{CheckUID: check.UID})
	r.NoError(err)

	return page
}

// --- Test 1: a hit inside the TTL does no work ---

// TestPageMemo_SecondReadInsideTTLIsFree is the whole point of the spec: the
// second reader of a page inside the freshness window costs nothing beyond the
// two lookups the gate needs.
func TestPageMemo_SecondReadInsideTTLIsFree(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, counting, org := memoTestSetup(t)
	page := seedMemoPage(ctx, t, svc, org)

	first, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
	r.NoError(err)
	r.Positive(counting.computeReads(), "a cold read must actually compute")

	counting.reset()

	second, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
	r.NoError(err)

	r.Zero(counting.computeReads(),
		"a warm read must run no sections/resources/updates query")
	r.Equal(int64(1), counting.orgLookups.Load(), "the org lookup still runs, every time")
	r.Equal(int64(1), counting.pageLookups.Load(), "the page lookup still runs, every time")

	r.Equal(first, second, "the memoized answer must equal the computed one")
}

// --- Test 2: the entry expires ---

// TestPageMemo_RecomputesAfterTTL pins the expiry with an injected clock rather
// than a sleep: a test that waited fifteen seconds would be deleted by the next
// person who ran the suite.
func TestPageMemo_RecomputesAfterTTL(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, counting, org := memoTestSetup(t)

	var nowNanos atomic.Int64

	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	nowNanos.Store(base.UnixNano())
	svc.memo.now = func() time.Time { return time.Unix(0, nowNanos.Load()).UTC() }

	page := seedMemoPage(ctx, t, svc, org)

	_, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
	r.NoError(err)

	// One nanosecond before the TTL: still a hit.
	nowNanos.Store(base.Add(statuspagecache.PageMemoTTL - time.Nanosecond).UnixNano())
	counting.reset()
	_, err = svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
	r.NoError(err)
	r.Zero(counting.computeReads(), "the entry must live for the full TTL")

	// Exactly at the TTL: expired.
	nowNanos.Store(base.Add(statuspagecache.PageMemoTTL).UnixNano())
	counting.reset()
	_, err = svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
	r.NoError(err)
	r.Positive(counting.computeReads(), "the entry must not outlive the TTL")
}

// --- Test 3: the gate runs before the memo, always ---

// TestPageMemo_GateRunsBeforeTheMemo is the security test of this spec.
//
// The memo makes one computed body reachable by more than one request, so the
// question it raises is not "is the body right" but "who can reach it". The
// answer has to be "only a request that passed the gate itself", and the way
// this test proves it is to warm the memo with a request that DID pass and then
// make one that did not:
//
//   - the second request must fail with the lock error, not return a body;
//   - it must not even reach the section fan-out, which is what shows the gate
//     ran ahead of everything and short-circuited;
//   - and the same must hold for the summary and the badge, which are separate
//     entry points into the same memo.
//
// A regression here is not a stale page. It is a password-protected page's
// contents served to somebody who never typed the password.
func TestPageMemo_GateRunsBeforeTheMemo(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, counting, org := memoTestSetup(t)

	page := createProtectedPage(ctx, t, svc)

	// Warm every product of the memo through a request that HAS the unlock
	// grant, exactly like a visitor who typed the password.
	unlocked := grantedCtx(ctx)

	warmView, err := svc.ViewStatusPage(unlocked, org.Slug, page.Slug, AllViewOptions())
	r.NoError(err)
	r.Equal(models.StatusPageVisibilityPassword, warmView.Visibility)

	_, err = svc.ViewStatusPageSummary(unlocked, org.Slug, page.Slug)
	r.NoError(err)

	_, err = svc.GenerateBadge(unlocked, org.Slug, page.Slug, BadgeOptions{})
	r.NoError(err)

	r.Positive(svc.memo.size(), "the memo must be warm for this test to prove anything")

	// Now the request that never passed the gate. Three surfaces, one rule.
	counting.reset()

	_, err = svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
	r.ErrorIs(err, statuspagelock.ErrLocked, "a warm memo must not unlock a password page")

	_, err = svc.ViewDefaultStatusPage(ctx, org.Slug, AllViewOptions())
	r.Error(err, "the default-page route must be gated identically")
	r.NotErrorIs(err, nil)

	_, err = svc.ViewStatusPageSummary(ctx, org.Slug, page.Slug)
	r.ErrorIs(err, statuspagelock.ErrLocked, "the summary must not answer from a warm memo either")

	_, err = svc.GenerateBadge(ctx, org.Slug, page.Slug, BadgeOptions{})
	r.ErrorIs(err, statuspagelock.ErrLocked, "the badge must not answer from a warm memo either")

	r.Zero(counting.computeReads(),
		"a gated request must be refused before any part of the view is touched, warm or cold")
	r.Positive(counting.pageLookups.Load(), "the page lookup is what the gate reads; it must still run")

	// The memo is untouched by the refusals — nothing was evicted, nothing was
	// added — so an authorized reader still gets the fast path afterwards.
	counting.reset()
	again, err := svc.ViewStatusPage(unlocked, org.Slug, page.Slug, AllViewOptions())
	r.NoError(err)
	r.Zero(counting.computeReads())
	r.Equal(warmView, again)
}

// TestPageMemo_VisibilityFlipIsHonouredImmediately covers the other half of
// "gate first": the gate reads the CURRENT page row, never the memoized body,
// so making a warm public page private hides it at once rather than in fifteen
// seconds.
//
// This is the scenario where a cache keyed on the page would be most tempting
// and most wrong: the operator's instinct when they take a page private is that
// it is private NOW.
func TestPageMemo_VisibilityFlipIsHonouredImmediately(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, counting, org := memoTestSetup(t)
	page := seedMemoPage(ctx, t, svc, org)

	_, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
	r.NoError(err)

	private := models.StatusPageVisibilityPrivate
	_, err = svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{Visibility: &private})
	r.NoError(err)

	counting.reset()

	_, err = svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
	r.ErrorIs(err, ErrStatusPageNotFound)
	r.Zero(counting.computeReads())

	// Disabling behaves the same way.
	public := models.StatusPageVisibilityPublic
	enabled := false
	_, err = svc.UpdateStatusPage(ctx, org.Slug, page.UID,
		&UpdateStatusPageRequest{Visibility: &public, Enabled: &enabled})
	r.NoError(err)

	_, err = svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
	r.ErrorIs(err, ErrStatusPageNotFound)
}

// --- Test 4: key variants ---

// TestPageMemo_KeyVariants pins what counts as the same view and what does not:
// two `include` shapes are two entries (or the narrow one would be served the
// wide one's payload), while the default-page route and the slug route are the
// same view reached two ways and must share one entry.
func TestPageMemo_KeyVariants(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, counting, org := memoTestSetup(t)
	page := seedMemoPage(ctx, t, svc, org)

	_, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
	r.NoError(err)

	// `include=` — neither optional section.
	narrow := ViewOptions{}

	counting.reset()
	_, err = svc.ViewStatusPage(ctx, org.Slug, page.Slug, narrow)
	r.NoError(err)
	r.Positive(counting.computeReads(), "a different include set is a different view")

	r.Len(svc.memo.memoKeysForPage(page.UID), 2,
		"two shapes are two entries, held: %s", svc.memo.memoDebugString())

	// The default-page route resolves to the same page and must hit the entry
	// the slug route just stored.
	isDefault := true
	_, err = svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{IsDefault: &isDefault})
	r.NoError(err)

	_, err = svc.ViewDefaultStatusPage(ctx, org.Slug, AllViewOptions())
	r.NoError(err)

	counting.reset()

	viaSlug, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
	r.NoError(err)
	r.Zero(counting.computeReads(), "the slug route must reuse what the default route computed")

	viaDefault, err := svc.ViewDefaultStatusPage(ctx, org.Slug, AllViewOptions())
	r.NoError(err)
	r.Equal(viaSlug, viaDefault)

	r.Len(svc.memo.memoKeysForPage(page.UID), 1,
		"the update above evicted, and both routes then shared ONE entry, held: %s",
		svc.memo.memoDebugString())
}

// --- Test 5: every write path evicts ---

// TestPageMemo_WritePathsEvict drives this package's own write paths end to end:
// warm the memo, perform the write, read again and assert the view was
// recomputed.
//
// The rows are the statuspages half of PageMemoWritePaths. The other packages'
// rows are covered by TestPageMemo_EveryWritePathSiteInvalidates (their
// eviction call, in the function the table names) plus their own package tests;
// standing a publication or an asset upload up from here would mean importing
// three services into this package's tests for what the scan already pins.
func TestPageMemo_WritePathsEvict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path  PageMemoWritePath
		write func(ctx context.Context, t *testing.T, svc *Service, org *models.Organization, page StatusPageResponse)
	}{
		{
			path: WritePathUpdateStatusPage,
			write: func(ctx context.Context, t *testing.T, svc *Service, org *models.Organization, page StatusPageResponse) {
				name := "Renamed"
				_, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{Name: &name})
				require.NoError(t, err)
			},
		},
		{
			path: WritePathDeleteStatusPage,
			write: func(ctx context.Context, t *testing.T, svc *Service, org *models.Organization, page StatusPageResponse) {
				require.NoError(t, svc.DeleteStatusPage(ctx, org.Slug, page.UID))
			},
		},
		{
			path: WritePathCreateSection,
			write: func(ctx context.Context, t *testing.T, svc *Service, org *models.Organization, page StatusPageResponse) {
				_, err := svc.CreateSection(ctx, org.Slug, page.UID,
					CreateSectionRequest{Name: "Extra", Slug: "extra"})
				require.NoError(t, err)
			},
		},
		{
			path: WritePathUpdateSection,
			write: func(ctx context.Context, t *testing.T, svc *Service, org *models.Organization, page StatusPageResponse) {
				section := firstSection(ctx, t, svc, page.UID)
				name := "Core renamed"
				_, err := svc.UpdateSection(ctx, org.Slug, page.UID, section.UID,
					UpdateSectionRequest{Name: &name})
				require.NoError(t, err)
			},
		},
		{
			path: WritePathDeleteSection,
			write: func(ctx context.Context, t *testing.T, svc *Service, org *models.Organization, page StatusPageResponse) {
				section := firstSection(ctx, t, svc, page.UID)
				require.NoError(t, svc.DeleteSection(ctx, org.Slug, page.UID, section.UID))
			},
		},
		{
			path: WritePathReorderSections,
			write: func(ctx context.Context, t *testing.T, svc *Service, org *models.Organization, page StatusPageResponse) {
				_, err := svc.CreateSection(ctx, org.Slug, page.UID,
					CreateSectionRequest{Name: "Second", Slug: "second"})
				require.NoError(t, err)

				sections, err := svc.db.ListStatusPageSections(ctx, page.UID)
				require.NoError(t, err)
				require.Len(t, sections, 2)

				// Warm AFTER the create above, so what this row proves is the
				// reorder's own eviction.
				_, err = svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
				require.NoError(t, err)

				require.NoError(t, svc.ReorderSections(ctx, org.Slug, page.UID,
					[]string{sections[1].UID, sections[0].UID}))
			},
		},
		{
			path: WritePathCreateResource,
			write: func(ctx context.Context, t *testing.T, svc *Service, org *models.Organization, page StatusPageResponse) {
				section := firstSection(ctx, t, svc, page.UID)
				check := models.NewCheck(org.UID, "second", "http")
				require.NoError(t, svc.db.CreateCheck(ctx, check))

				_, err := svc.CreateResource(ctx, org.Slug, page.UID, section.UID,
					CreateResourceRequest{CheckUID: check.UID})
				require.NoError(t, err)
			},
		},
		{
			path: WritePathUpdateResource,
			write: func(ctx context.Context, t *testing.T, svc *Service, org *models.Organization, page StatusPageResponse) {
				section := firstSection(ctx, t, svc, page.UID)
				resource := firstResource(ctx, t, svc, section.UID)
				publicName := "Public API"
				_, err := svc.UpdateResource(ctx, org.Slug, page.UID, section.UID, resource.UID,
					UpdateResourceRequest{PublicName: &publicName})
				require.NoError(t, err)
			},
		},
		{
			path: WritePathDeleteResource,
			write: func(ctx context.Context, t *testing.T, svc *Service, org *models.Organization, page StatusPageResponse) {
				section := firstSection(ctx, t, svc, page.UID)
				resource := firstResource(ctx, t, svc, section.UID)
				require.NoError(t, svc.DeleteResource(ctx, org.Slug, page.UID, section.UID, resource.UID))
			},
		},
		{
			path: WritePathReorderResources,
			write: func(ctx context.Context, t *testing.T, svc *Service, org *models.Organization, page StatusPageResponse) {
				section := firstSection(ctx, t, svc, page.UID)
				check := models.NewCheck(org.UID, "second", "http")
				require.NoError(t, svc.db.CreateCheck(ctx, check))
				_, err := svc.CreateResource(ctx, org.Slug, page.UID, section.UID,
					CreateResourceRequest{CheckUID: check.UID})
				require.NoError(t, err)

				resources, err := svc.db.ListStatusPageResources(ctx, section.UID)
				require.NoError(t, err)
				require.Len(t, resources, 2)

				_, err = svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
				require.NoError(t, err)

				require.NoError(t, svc.ReorderResources(ctx, org.Slug, page.UID, section.UID,
					[]string{resources[1].UID, resources[0].UID}))
			},
		},
		{
			path: WritePathSetCustomDomain,
			write: func(ctx context.Context, t *testing.T, svc *Service, org *models.Organization, page StatusPageResponse) {
				domain := "status.acme.com"
				_, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID,
					&UpdateStatusPageRequest{CustomDomain: &domain, CustomDomainSet: true})
				require.NoError(t, err)
			},
		},
		{
			path: WritePathClearCustomDomain,
			write: func(ctx context.Context, t *testing.T, svc *Service, org *models.Organization, page StatusPageResponse) {
				domain := "status.acme.com"
				_, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID,
					&UpdateStatusPageRequest{CustomDomain: &domain, CustomDomainSet: true})
				require.NoError(t, err)

				_, err = svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
				require.NoError(t, err)

				empty := ""
				_, err = svc.UpdateStatusPage(ctx, org.Slug, page.UID,
					&UpdateStatusPageRequest{CustomDomain: &empty, CustomDomainSet: true})
				require.NoError(t, err)
			},
		},
		{
			path: WritePathSelectorReconcile,
			write: func(ctx context.Context, t *testing.T, svc *Service, org *models.Organization, page StatusPageResponse) {
				// Give the page a selector section and a check it matches, then
				// let the reconcile materialize the row. The reconcile is the
				// write here — nothing else touches the page.
				section, err := svc.CreateSection(ctx, org.Slug, page.UID,
					CreateSectionRequest{Name: "Dynamic", Slug: "dynamic", Selector: []byte(`{"all":true}`)})
				require.NoError(t, err)
				require.NotEmpty(t, section.UID)

				check := models.NewCheck(org.UID, "late", "http")
				require.NoError(t, svc.db.CreateCheck(ctx, check))

				// Warm with the check already created but not yet materialized,
				// and clear the backstop mark so the next view reconciles.
				_, err = svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
				require.NoError(t, err)
				svc.reconcileMarks.invalidate(page.UID)
			},
		},
	}

	for _, testCase := range tests {
		t.Run(string(testCase.path), func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx, svc, counting, org := memoTestSetup(t)
			page := seedMemoPage(ctx, t, svc, org)

			_, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
			r.NoError(err)

			testCase.write(ctx, t, svc, org, page)

			counting.reset()

			_, viewErr := svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())

			// A deleted page cannot be read back; the eviction is what the
			// memo's own bookkeeping shows.
			if testCase.path == WritePathDeleteStatusPage {
				r.Error(viewErr)
				r.Empty(svc.memo.memoKeysForPage(page.UID),
					"the deleted page must hold no memoized view, held: %s", svc.memo.memoDebugString())

				return
			}

			r.NoError(viewErr)
			r.Positive(counting.computeReads(),
				"%s must evict: the read after it recomputed nothing", testCase.path)
		})
	}
}

// firstSection returns the page's first section.
func firstSection(
	ctx context.Context, t *testing.T, svc *Service, pageUID string,
) *models.StatusPageSection {
	t.Helper()

	sections, err := svc.db.ListStatusPageSections(ctx, pageUID)
	require.NoError(t, err)
	require.NotEmpty(t, sections)

	return sections[0]
}

// firstResource returns the section's first resource.
func firstResource(
	ctx context.Context, t *testing.T, svc *Service, sectionUID string,
) *models.StatusPageResource {
	t.Helper()

	resources, err := svc.db.ListStatusPageResources(ctx, sectionUID)
	require.NoError(t, err)
	require.NotEmpty(t, resources)

	return resources[0]
}

// TestPageMemo_EveryWritePathSiteInvalidates is the completeness half of the
// invalidation table: for every row, the named function must still contain an
// eviction call.
//
// It parses the source rather than asserting behaviour because the rows that
// most need guarding live in other packages, where a refactor can quietly drop
// the one line that matters and every test in sight stays green. A failure here
// names the file and function, which is all anybody needs.
func TestPageMemo_EveryWritePathSiteInvalidates(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.NotEmpty(PageMemoWritePaths)

	seen := make(map[PageMemoWritePath]bool, len(PageMemoWritePaths))

	for _, site := range PageMemoWritePaths {
		r.False(seen[site.Path], "duplicate row for %s", site.Path)
		seen[site.Path] = true

		// The constant's value is the qualified name; the row's Func must be
		// its last segment, so a copy-pasted row cannot point at the wrong
		// function.
		qualified := string(site.Path)
		r.True(strings.HasSuffix(qualified, "."+site.Func),
			"row %s names function %q, which does not match the constant", site.Path, site.Func)

		path := filepath.Join("..", "..", "..", site.File)

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		r.NoError(err, "row %s points at %s", site.Path, site.File)

		found := false
		invalidates := false

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != site.Func || fn.Body == nil {
				continue
			}

			found = true
			invalidates = bodyCallsAny(fn.Body, PageMemoEvictionCalls)
		}

		r.True(found, "row %s: %s has no function named %s", site.Path, site.File, site.Func)
		r.True(invalidates,
			"row %s: %s.%s no longer evicts the page memo — one of %v has to be called there",
			site.Path, site.File, site.Func, PageMemoEvictionCalls)
	}
}

// bodyCallsAny reports whether the function body calls a method with one of the
// given names.
func bodyCallsAny(body *ast.BlockStmt, names []string) bool {
	called := false

	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		for _, name := range names {
			if sel.Sel.Name == name {
				called = true

				return false
			}
		}

		return true
	})

	return called
}

// --- Test 6: singleflight ---

// TestPageMemo_SingleflightCollapsesConcurrentMisses is the incident load shape:
// everybody opens the page at once, on a cold memo, and the database sees ONE
// computation.
func TestPageMemo_SingleflightCollapsesConcurrentMisses(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	memo := newPageMemo()
	key := pageViewKey("org", "page", AllViewOptions())

	release := make(chan struct{})

	var computations atomic.Int64

	const callers = 50

	results := make([]StatusPageResponse, callers)
	errs := make([]error, callers)

	var wg sync.WaitGroup

	for i := range callers {
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()

			results[idx], errs[idx] = memoDo(memo, key, func() (StatusPageResponse, error) {
				computations.Add(1)
				<-release

				return StatusPageResponse{UID: "computed"}, nil
			})
		}(i)
	}

	// Let every goroutine pile onto the same key before the one computation is
	// allowed to finish. Without singleflight this is fifty computations.
	waitFor(t, func() bool { return computations.Load() >= 1 })
	close(release)
	wg.Wait()

	r.Equal(int64(1), computations.Load(), "fifty concurrent misses must cost one computation")

	for i := range callers {
		r.NoError(errs[i])
		r.Equal("computed", results[i].UID, "every waiter must get the computed answer")
	}
}

// TestPageMemo_ErrorIsNotCachedOrPoisoned pins that a failed computation is
// neither stored nor sticky. A status page that failed to render once has to be
// retried by the next reader — caching the failure would turn one bad query
// into fifteen seconds of outage, and singleflight poisoning would turn it into
// a permanent one.
func TestPageMemo_ErrorIsNotCachedOrPoisoned(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	memo := newPageMemo()
	key := summaryKey("org", "page", true)
	boom := context.DeadlineExceeded

	_, err := memoDo(memo, key, func() (StatusPageSummary, error) {
		return StatusPageSummary{}, boom
	})
	r.ErrorIs(err, boom)
	r.Zero(memo.size(), "an erroring computation must store nothing")

	// The next caller computes again, and succeeds.
	summary, err := memoDo(memo, key, func() (StatusPageSummary, error) {
		return StatusPageSummary{PageName: "Acme"}, nil
	})
	r.NoError(err)
	r.Equal("Acme", summary.PageName)
	r.Equal(1, memo.size())
}

// --- Test 7: the summary and the badge share one entry ---

// TestPageMemo_BadgeReusesTheSummary pins that the badge — the hottest caller
// of the summary, embedded in READMEs — costs nothing after a summary read.
func TestPageMemo_BadgeReusesTheSummary(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, counting, org := memoTestSetup(t)
	page := seedMemoPage(ctx, t, svc, org)

	// The badge asks for withAvailability=false, so warm THAT shape the way the
	// badge itself does, then assert the second badge render is free.
	_, err := svc.GenerateBadge(ctx, org.Slug, page.Slug, BadgeOptions{})
	r.NoError(err)

	counting.reset()

	badge, err := svc.GenerateBadge(ctx, org.Slug, page.Slug, BadgeOptions{})
	r.NoError(err)
	r.Contains(badge.SVG, "<svg")
	r.Zero(counting.computeReads(), "a warm badge must run no enrichment")

	// The JSON summary asks for the availability number, which the badge
	// deliberately does not pay for — so it is a different entry and computes.
	counting.reset()
	_, err = svc.ViewStatusPageSummary(ctx, org.Slug, page.Slug)
	r.NoError(err)
	r.Positive(counting.computeReads(),
		"the summary asks for more than the badge and must not be served the badge's answer")

	// And the summary is now warm for its own shape.
	counting.reset()
	_, err = svc.ViewStatusPageSummary(ctx, org.Slug, page.Slug)
	r.NoError(err)
	r.Zero(counting.computeReads())
}

// --- Bounds ---

// TestPageMemo_SweepsOldestPastTheBound pins the blast-radius limit: the map
// never grows past memoMaxEntries, and what goes first is the oldest entry.
func TestPageMemo_SweepsOldestPastTheBound(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	memo := newPageMemo()

	var nowNanos atomic.Int64

	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	nowNanos.Store(base.UnixNano())
	memo.now = func() time.Time { return time.Unix(0, nowNanos.Load()).UTC() }

	// Insert one more than the bound, each a millisecond newer than the last so
	// "oldest" is unambiguous. All inside the TTL, so nothing expires and the
	// sweep has to fall through to age.
	pageUID := func(i int) string { return "page-" + strconv.Itoa(i) }

	for i := range memoMaxEntries + 1 {
		nowNanos.Store(base.Add(time.Duration(i) * time.Millisecond).UnixNano())
		memo.store(pageViewKey("org", pageUID(i), AllViewOptions()),
			StatusPageResponse{UID: strconv.Itoa(i)})
	}

	r.Equal(memoMaxEntries, memo.size(), "the map must not grow past its bound")

	_, found := memo.lookup(pageViewKey("org", pageUID(0), AllViewOptions()))
	r.False(found, "the oldest entry is what the sweep drops")

	_, found = memo.lookup(pageViewKey("org", pageUID(memoMaxEntries), AllViewOptions()))
	r.True(found, "the newest entry must survive")
}

// TestPageMemo_InvalidateOrgDropsEveryPage pins the coarse eviction used by a
// write path that cannot name the page it changed, and that it stops at the
// organization boundary.
func TestPageMemo_InvalidateOrgDropsEveryPage(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	memo := newPageMemo()
	memo.store(pageViewKey("org-a", "page-1", AllViewOptions()), StatusPageResponse{UID: "1"})
	memo.store(summaryKey("org-a", "page-2", true), StatusPageSummary{PageName: "2"})
	memo.store(pageViewKey("org-b", "page-3", AllViewOptions()), StatusPageResponse{UID: "3"})

	memo.invalidateOrg("org-a")

	r.Equal(1, memo.size())

	_, found := memo.lookup(pageViewKey("org-b", "page-3", AllViewOptions()))
	r.True(found, "another organization's entries must survive")
}

// waitFor polls cond until it holds, failing the test rather than hanging
// forever if it never does.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(time.Millisecond)
	}

	t.Fatal("condition never held")
}
