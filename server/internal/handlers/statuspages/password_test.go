package statuspages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/statuspagelock"
)

const testPagePassword = "correct-horse"

func passwordSetup(t *testing.T) (context.Context, db.Service, *Service, *models.Organization) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbService.Initialize(ctx))
	t.Cleanup(func() { _ = dbService.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbService.CreateOrganization(ctx, org))

	return ctx, dbService, NewService(dbService, &config.Config{}, nil), org
}

// createProtectedPage builds a live page behind a password, through the public
// API surface (CreateStatusPage) rather than by writing the row directly, so
// the test also exercises hashing and validation.
func createProtectedPage(ctx context.Context, t *testing.T, svc *Service) StatusPageResponse {
	t.Helper()

	visibility := models.StatusPageVisibilityPassword
	password := testPagePassword

	page, err := svc.CreateStatusPage(ctx, "acme", &CreateStatusPageRequest{
		Name:       "Acme Status",
		Slug:       testPublicSlug,
		Visibility: &visibility,
		Password:   &password,
	})
	require.NoError(t, err)

	return page
}

// grantedCtx simulates a request that arrived with a valid unlock cookie.
func grantedCtx(ctx context.Context) context.Context {
	return statuspagelock.WithGrant(ctx, func(*models.StatusPage) bool { return true })
}

// TestPasswordPageIsLockedNotMissing is the core distinction the feature
// rests on: `password` answers 401, `private` answers 404. Conflating them
// would make the page unshareable (a customer cannot unlock what looks absent).
func TestPasswordPageIsLockedNotMissing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, dbService, svc, org := passwordSetup(t)
	createProtectedPage(ctx, t, svc)

	_, err := svc.ViewStatusPage(ctx, "acme", testPublicSlug)
	r.ErrorIs(err, statuspagelock.ErrLocked)

	// Negative control: the same page made `private` 404s instead.
	page, err := dbService.GetStatusPageBySlug(ctx, org.UID, testPublicSlug)
	r.NoError(err)

	private := models.StatusPageVisibilityPrivate
	r.NoError(dbService.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{Visibility: &private}))

	_, err = svc.ViewStatusPage(ctx, "acme", testPublicSlug)
	r.ErrorIs(err, ErrStatusPageNotFound)
}

// TestUnlockedContextSeesThePage is the positive control for the gate: with a
// grant that says yes, the very same call returns the page.
func TestUnlockedContextSeesThePage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, _, svc, _ := passwordSetup(t)
	createProtectedPage(ctx, t, svc)

	page, err := svc.ViewStatusPage(grantedCtx(ctx), "acme", testPublicSlug)
	r.NoError(err)
	r.Equal(testPublicSlug, page.Slug)
	r.True(page.HasPassword)
}

// TestEveryPublicReadIsGated walks the public read surface. A gate that
// covered the page view but left the summary, the badge or the incident feed
// open would leak exactly the state the password protects.
func TestEveryPublicReadIsGated(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, _, svc, _ := passwordSetup(t)
	createProtectedPage(ctx, t, svc)

	_, err := svc.ViewStatusPage(ctx, "acme", testPublicSlug)
	r.ErrorIs(err, statuspagelock.ErrLocked, "page view")

	_, err = svc.ViewStatusPageSummary(ctx, "acme", testPublicSlug)
	r.ErrorIs(err, statuspagelock.ErrLocked, "summary")

	_, err = svc.GenerateBadge(ctx, "acme", testPublicSlug, BadgeOptions{})
	r.ErrorIs(err, statuspagelock.ErrLocked, "badge")

	_, err = svc.ViewDefaultStatusPage(ctx, "acme")
	r.ErrorIs(err, statuspagelock.ErrLocked, "default page view")

	// Positive controls: all four succeed once unlocked, so the assertions
	// above are proving the gate rather than a broken fixture.
	granted := grantedCtx(ctx)

	_, err = svc.ViewStatusPage(granted, "acme", testPublicSlug)
	r.NoError(err)

	_, err = svc.ViewStatusPageSummary(granted, "acme", testPublicSlug)
	r.NoError(err)

	_, err = svc.GenerateBadge(granted, "acme", testPublicSlug, BadgeOptions{})
	r.NoError(err)

	_, err = svc.ViewDefaultStatusPage(granted, "acme")
	r.NoError(err)
}

// TestUnlockMintsAUsableToken drives the real flow end to end: the right
// password produces a token that the cookie check accepts, and the wrong one
// produces nothing.
func TestUnlockMintsAUsableToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, dbService, svc, org := passwordSetup(t)
	createProtectedPage(ctx, t, svc)

	_, err := svc.Unlock(ctx, "acme", testPublicSlug, "203.0.113.1", "wrong")
	r.ErrorIs(err, ErrWrongPassword)

	result, err := svc.Unlock(ctx, "acme", testPublicSlug, "203.0.113.1", testPagePassword)
	r.NoError(err)
	r.NotEmpty(result.Token)

	page, err := dbService.GetStatusPageBySlug(ctx, org.UID, testPublicSlug)
	r.NoError(err)

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: statuspagelock.CookieName(page.UID), Value: result.Token})
	r.True(statuspagelock.RequestUnlocks(req, page))

	// ...and the page really renders behind that request's grant.
	view, err := svc.ViewStatusPage(statuspagelock.WithGrant(ctx, statuspagelock.FromRequest(req)), "acme", testPublicSlug)
	r.NoError(err)
	r.Equal(testPublicSlug, view.Slug)
}

// TestChangingThePasswordInvalidatesOutstandingUnlocks is the property that
// justifies deriving the cookie key from the stored hash: revocation is free.
func TestChangingThePasswordInvalidatesOutstandingUnlocks(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, dbService, svc, org := passwordSetup(t)
	createProtectedPage(ctx, t, svc)

	result, err := svc.Unlock(ctx, "acme", testPublicSlug, "203.0.113.1", testPagePassword)
	r.NoError(err)

	page, err := dbService.GetStatusPageBySlug(ctx, org.UID, testPublicSlug)
	r.NoError(err)

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: statuspagelock.CookieName(page.UID), Value: result.Token})
	r.True(statuspagelock.RequestUnlocks(req, page), "positive control before the rotation")

	newPassword := "a-different-secret"
	_, err = svc.UpdateStatusPage(ctx, "acme", testPublicSlug, &UpdateStatusPageRequest{Password: &newPassword})
	r.NoError(err)

	rotated, err := dbService.GetStatusPageBySlug(ctx, org.UID, testPublicSlug)
	r.NoError(err)
	r.False(statuspagelock.RequestUnlocks(req, rotated), "the old cookie must stop working")
}

// TestPasswordVisibilityRequiresAPassword covers both halves of the rule: you
// cannot switch a page to `password` without one, and you cannot strip the
// password off a page that is still in that mode (which would leave a page
// nobody can open).
func TestPasswordVisibilityRequiresAPassword(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, _, svc, _ := passwordSetup(t)

	visibility := models.StatusPageVisibilityPassword

	_, err := svc.CreateStatusPage(ctx, "acme", &CreateStatusPageRequest{
		Name: "No Password", Slug: "no-password", Visibility: &visibility,
	})
	r.ErrorIs(err, ErrPasswordRequired)

	createProtectedPage(ctx, t, svc)

	empty := ""
	_, err = svc.UpdateStatusPage(ctx, "acme", testPublicSlug, &UpdateStatusPageRequest{Password: &empty})
	r.ErrorIs(err, ErrPasswordRequired)

	// Leaving `password` visibility, on the other hand, may clear it.
	public := models.StatusPageVisibilityPublic
	_, err = svc.UpdateStatusPage(ctx, "acme", testPublicSlug, &UpdateStatusPageRequest{
		Visibility: &public, Password: &empty,
	})
	r.NoError(err)

	page, err := svc.ViewStatusPage(ctx, "acme", testPublicSlug)
	r.NoError(err)
	r.False(page.HasPassword)
}

// TestSavingAnUnrelatedFieldKeepsThePassword: re-saving the form with an empty
// password field must not silently unlock the page.
func TestSavingAnUnrelatedFieldKeepsThePassword(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, _, svc, _ := passwordSetup(t)
	createProtectedPage(ctx, t, svc)

	name := "Renamed"
	_, err := svc.UpdateStatusPage(ctx, "acme", testPublicSlug, &UpdateStatusPageRequest{Name: &name})
	r.NoError(err)

	_, err = svc.ViewStatusPage(ctx, "acme", testPublicSlug)
	r.ErrorIs(err, statuspagelock.ErrLocked)
}

// TestInvalidVisibilityIsRejected keeps the free-form column honest — there is
// no database check constraint, so this validation IS the gate.
func TestInvalidVisibilityIsRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, _, svc, _ := passwordSetup(t)

	bogus := "unlisted"
	_, err := svc.CreateStatusPage(ctx, "acme", &CreateStatusPageRequest{
		Name: "Bogus", Slug: "bogus", Visibility: &bogus,
	})
	r.ErrorIs(err, ErrInvalidVisibility)
}

// TestShortPasswordIsRejected pins the length floor.
func TestShortPasswordIsRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, _, svc, _ := passwordSetup(t)

	visibility := models.StatusPageVisibilityPassword
	short := "abc"

	_, err := svc.CreateStatusPage(ctx, "acme", &CreateStatusPageRequest{
		Name: "Short", Slug: "short", Visibility: &visibility, Password: &short,
	})
	r.ErrorIs(err, ErrPasswordTooShort)
}

// TestUnlockIsRateLimited proves the brute-force ceiling exists, and that it
// is keyed per (IP, page) rather than globally — a second page must not be
// collateral damage of the first one's attempts.
func TestUnlockIsRateLimited(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, _, svc, _ := passwordSetup(t)
	createProtectedPage(ctx, t, svc)

	visibility := models.StatusPageVisibilityPassword
	password := testPagePassword
	_, err := svc.CreateStatusPage(ctx, "acme", &CreateStatusPageRequest{
		Name: "Second", Slug: "second", Visibility: &visibility, Password: &password,
	})
	r.NoError(err)

	var lastErr error
	for range unlockAttemptsPerWindow + 1 {
		_, lastErr = svc.Unlock(ctx, "acme", testPublicSlug, "198.51.100.7", "guess")
	}

	r.ErrorIs(lastErr, ErrTooManyUnlockAttempts)

	// Same IP, different page: still allowed, and still able to succeed.
	_, err = svc.Unlock(ctx, "acme", "second", "198.51.100.7", testPagePassword)
	r.NoError(err)
}

// TestExpiredAndForgedTokensAreRejected covers the token format's own guards
// without going through the service.
func TestExpiredAndForgedTokensAreRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	const hash = "$argon2id$fake-but-stable"

	now := time.Now()

	valid := statuspagelock.Issue(hash, "page-1", now, time.Hour)
	r.True(statuspagelock.Valid(hash, "page-1", valid, now))

	expired := statuspagelock.Issue(hash, "page-1", now.Add(-2*time.Hour), time.Hour)
	r.False(statuspagelock.Valid(hash, "page-1", expired, now), "an expired token must not unlock")

	r.False(statuspagelock.Valid(hash, "page-2", valid, now), "a token for one page must not unlock another")
	r.False(statuspagelock.Valid("other-hash", "page-1", valid, now), "a token must not survive a password change")
	r.False(statuspagelock.Valid(hash, "page-1", "9999999999.forged", now))
	r.False(statuspagelock.Valid(hash, "page-1", "not-a-token", now))
}

// TestNoGrantMeansDenied pins the deny-by-default: a caller that never went
// through the request middleware (MCP, a job) cannot read a protected page.
func TestNoGrantMeansDenied(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	page := &models.StatusPage{
		UID:          "page-1",
		Enabled:      true,
		Visibility:   models.StatusPageVisibilityPassword,
		PasswordHash: strPointer("$argon2id$whatever"),
	}

	r.False(statuspagelock.Allows(context.Background(), page))

	// A page that is not protected needs no grant at all.
	page.Visibility = models.StatusPageVisibilityPublic
	r.True(statuspagelock.Allows(context.Background(), page))
}

// TestPasswordPageWithNoHashIsNotServable covers the inconsistent row: a
// `password` page whose hash went missing must fail closed, not fall back to
// public.
func TestPasswordPageWithNoHashIsNotServable(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	page := &models.StatusPage{
		UID:        "page-1",
		Enabled:    true,
		Visibility: models.StatusPageVisibilityPassword,
	}

	r.False(statuspagelock.Visible(page))
	r.False(statuspagelock.Protected(page))
}

func strPointer(s string) *string { return &s }
