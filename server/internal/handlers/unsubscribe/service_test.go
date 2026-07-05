package unsubscribe_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/unsubscribe"
	"github.com/fclairamb/solidping/server/internal/incidentlinks"
)

const testSecret = "test-secret-key"

// testFixture bundles an in-memory db.Service with a ready org+check, so
// each test just needs to mint a token and call the service.
type testFixture struct {
	svc   db.Service
	org   *models.Organization
	check *models.Check
}

func newTestFixture(t *testing.T) *testFixture {
	t.Helper()

	r := require.New(t)

	svc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })

	r.NoError(svc.Initialize(t.Context()))

	org := models.NewOrganization("unsub-test-org", "")
	r.NoError(svc.CreateOrganization(t.Context(), org))

	check := models.NewCheck(org.UID, "unsub-test-check", "http")
	r.NoError(svc.CreateCheck(t.Context(), check))

	return &testFixture{svc: svc, org: org, check: check}
}

func (f *testFixture) checkToken(email string, exp time.Time) string {
	return incidentlinks.SignUnsubscribe([]byte(testSecret), f.org.Slug, email, f.check.UID, exp)
}

func (f *testFixture) orgWideToken(email string, exp time.Time) string {
	return incidentlinks.SignUnsubscribe([]byte(testSecret), f.org.Slug, email, "", exp)
}

func TestService_Unsubscribe_CheckScoped(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	svc := unsubscribe.NewService(f.svc, []byte(testSecret))

	token := f.checkToken("alice@example.com", time.Now().Add(time.Hour))

	res, err := svc.Unsubscribe(t.Context(), token, unsubscribe.ScopeFromToken, models.EmailSuppressionSourceLink)
	r.NoError(err)
	r.Equal("alice@example.com", res.Email)
	r.Equal(f.check.UID, res.CheckUID)
	r.NotEmpty(res.SuppressionUID)

	suppressed, err := f.svc.IsEmailSuppressed(t.Context(), f.org.UID, "alice@example.com", f.check.UID)
	r.NoError(err)
	r.True(suppressed, "the check must now be suppressed for this email")
}

func TestService_Unsubscribe_OrgWide(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	svc := unsubscribe.NewService(f.svc, []byte(testSecret))

	token := f.orgWideToken("bob@example.com", time.Now().Add(time.Hour))

	res, err := svc.Unsubscribe(t.Context(), token, unsubscribe.ScopeFromToken, models.EmailSuppressionSourceHeader)
	r.NoError(err)
	r.Empty(res.CheckUID, "org-wide unsubscribe has no check scope")

	suppressed, err := f.svc.IsEmailSuppressed(t.Context(), f.org.UID, "bob@example.com", f.check.UID)
	r.NoError(err)
	r.True(suppressed, "org-wide suppression must suppress every check")
}

// TestService_Unsubscribe_OneClickIdempotent is acceptance criterion 4's
// idempotency requirement: RFC 8058 one-click POSTs may be retried by the
// mail client (network hiccup, duplicate delivery) and must not error on the
// second submission, nor create a second row.
func TestService_Unsubscribe_OneClickIdempotent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	svc := unsubscribe.NewService(f.svc, []byte(testSecret))

	token := f.checkToken("idempotent@example.com", time.Now().Add(time.Hour))

	first, err := svc.Unsubscribe(t.Context(), token, unsubscribe.ScopeFromToken, models.EmailSuppressionSourceHeader)
	r.NoError(err)

	second, err := svc.Unsubscribe(t.Context(), token, unsubscribe.ScopeFromToken, models.EmailSuppressionSourceHeader)
	r.NoError(err, "a retried one-click POST must not error")
	r.Equal(first.SuppressionUID, second.SuppressionUID, "must resolve to the same row, not create a duplicate")

	rows, err := f.svc.ListEmailSuppressions(t.Context(), f.org.UID)
	r.NoError(err)
	r.Len(rows, 1, "exactly one suppression row must exist after two identical submissions")
}

func TestService_Unsubscribe_ExpiredToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	svc := unsubscribe.NewService(f.svc, []byte(testSecret))

	token := f.checkToken("expired@example.com", time.Now().Add(-time.Hour))

	_, err := svc.Unsubscribe(t.Context(), token, unsubscribe.ScopeFromToken, models.EmailSuppressionSourceLink)
	r.ErrorIs(err, unsubscribe.ErrTokenInvalid)

	rows, err := f.svc.ListEmailSuppressions(t.Context(), f.org.UID)
	r.NoError(err)
	r.Empty(rows, "an expired token must not create any suppression")
}

func TestService_Unsubscribe_TamperedToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	svc := unsubscribe.NewService(f.svc, []byte(testSecret))

	token := f.checkToken("tampered@example.com", time.Now().Add(time.Hour))
	tampered := token[:len(token)-4] + "abcd" // corrupt the signature tail

	_, err := svc.Unsubscribe(t.Context(), tampered, unsubscribe.ScopeFromToken, models.EmailSuppressionSourceLink)
	r.ErrorIs(err, unsubscribe.ErrTokenInvalid)

	rows, err := f.svc.ListEmailSuppressions(t.Context(), f.org.UID)
	r.NoError(err)
	r.Empty(rows, "a tampered token must not create any suppression")
}

func TestService_Unsubscribe_UnknownOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	svc := unsubscribe.NewService(f.svc, []byte(testSecret))

	token := incidentlinks.SignUnsubscribe(
		[]byte(testSecret), "does-not-exist-org", "ghost@example.com", "", time.Now().Add(time.Hour))

	_, err := svc.Unsubscribe(t.Context(), token, unsubscribe.ScopeFromToken, models.EmailSuppressionSourceLink)
	r.ErrorIs(err, unsubscribe.ErrOrgNotFound)
}

// TestService_Unsubscribe_ScopeOverride_WidenToOrg confirms the GET
// confirmation page's "all alerts" button (scope=org) widens a check-scoped
// token to an org-wide suppression, not just re-applying the token's own
// (narrower) scope.
func TestService_Unsubscribe_ScopeOverride_WidenToOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	svc := unsubscribe.NewService(f.svc, []byte(testSecret))

	otherCheck := models.NewCheck(f.org.UID, "unsub-test-other-check", "http")
	r.NoError(f.svc.CreateCheck(t.Context(), otherCheck))

	token := f.checkToken("widen@example.com", time.Now().Add(time.Hour))

	res, err := svc.Unsubscribe(t.Context(), token, unsubscribe.ScopeOrg, models.EmailSuppressionSourceLink)
	r.NoError(err)
	r.Empty(res.CheckUID, "scope=org must widen to an org-wide suppression")

	suppressed, err := f.svc.IsEmailSuppressed(t.Context(), f.org.UID, "widen@example.com", otherCheck.UID)
	r.NoError(err)
	r.True(suppressed, "the widened org-wide suppression must cover a check not named in the original token")
}

func TestService_ResubscribeByUID(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	svc := unsubscribe.NewService(f.svc, []byte(testSecret))

	token := f.checkToken("undo@example.com", time.Now().Add(time.Hour))

	res, err := svc.Unsubscribe(t.Context(), token, unsubscribe.ScopeFromToken, models.EmailSuppressionSourceLink)
	r.NoError(err)

	suppressed, err := f.svc.IsEmailSuppressed(t.Context(), f.org.UID, "undo@example.com", f.check.UID)
	r.NoError(err)
	r.True(suppressed)

	r.NoError(svc.ResubscribeByUID(t.Context(), token, res.SuppressionUID))

	suppressed, err = f.svc.IsEmailSuppressed(t.Context(), f.org.UID, "undo@example.com", f.check.UID)
	r.NoError(err)
	r.False(suppressed, "resubscribe must remove the suppression")
}

// TestService_ResubscribeByUID_WrongEmailRejected confirms the undo path
// re-verifies the token's email against the row being deleted — a forged
// undo link (same UID, different token/email) must not delete someone
// else's suppression.
func TestService_ResubscribeByUID_WrongEmailRejected(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	svc := unsubscribe.NewService(f.svc, []byte(testSecret))

	victimToken := f.checkToken("victim@example.com", time.Now().Add(time.Hour))
	res, err := svc.Unsubscribe(t.Context(), victimToken, unsubscribe.ScopeFromToken, models.EmailSuppressionSourceLink)
	r.NoError(err)

	// A different, validly-signed token (different email) must not be able
	// to resubscribe the victim's row just by supplying its UID.
	attackerToken := f.checkToken("attacker@example.com", time.Now().Add(time.Hour))

	err = svc.ResubscribeByUID(t.Context(), attackerToken, res.SuppressionUID)
	r.ErrorIs(err, unsubscribe.ErrTokenInvalid)

	suppressed, err := f.svc.IsEmailSuppressed(t.Context(), f.org.UID, "victim@example.com", f.check.UID)
	r.NoError(err)
	r.True(suppressed, "the victim's suppression must remain intact")
}
