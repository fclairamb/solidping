package emailcheck

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jmap"
)

func (f *authFixture) countResults(t *testing.T) int {
	t.Helper()

	n, err := f.svc.DB().NewSelect().Model((*models.Result)(nil)).
		Where("check_uid = ?", f.check.UID).
		Where("json_extract(output, '$.from') IS NOT NULL").Count(t.Context())
	require.NoError(t, err)

	return n
}

func TestHandleEmailVerifiesDKIMThroughBlobFetcher(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	raw, dnsKey := signedMessage(t, "hello")
	f := newAuthFixture(t, models.JSONMap{"require_dkim": true},
		&stubResolver{txt: map[string][]string{"s1._domainkey.example.com": {dnsKey}}})

	var fetched string

	f.h.SetBlobFetcher(func(_ context.Context, blobID string) ([]byte, error) {
		fetched = blobID

		return raw, nil
	})

	res := f.deliver(t, testToken+"@inbox.example.com", "<dkim@x>")

	r.Equal("blob1", fetched)
	r.Equal(int(checkerdef.StatusUp), *res.Status)

	auth, ok := res.Output["auth"].(map[string]any)
	r.True(ok)
	r.Equal("pass", auth["dkim"])
	r.Equal(true, auth["dkim_aligned"])
}

func TestHandleEmailDKIMWithoutBlobFetcherStaysUp(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{"require_dkim": true}, &stubResolver{})
	res := f.deliver(t, testToken+"@inbox.example.com", "<noblob@x>")

	r.Equal(int(checkerdef.StatusUp), *res.Status)

	auth, ok := res.Output["auth"].(map[string]any)
	r.True(ok)
	r.Equal("temperror", auth["dkim"])
}

func TestHandleEmailFailedRequirementRecordsDown(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{"require_spf": "pass"},
		&stubResolver{txt: map[string][]string{"example.com": {"v=spf1 -all"}}})
	res := f.deliver(t, testToken+"@inbox.example.com", "<spf@x>")

	r.Equal(int(checkerdef.StatusDown), *res.Status)
	r.Equal("delivered, but SPF fail for example.com", res.Output["message"])
}

func TestHandleEmailInvalidAuthConfigSkipsEvaluation(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{"require_spf": 5}, &stubResolver{})
	res := f.deliver(t, testToken+"@inbox.example.com", "<badcfg@x>")

	r.Equal(int(checkerdef.StatusUp), *res.Status)
	r.NotContains(res.Output, "auth")
}

func TestHandleEmailIgnoresMessagesWithoutToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{}, &stubResolver{})
	email := jmap.Email{To: []jmap.EmailAddress{{Email: "someone@inbox.example.com"}}}

	r.False(f.h.ClaimsEmail(email))

	outcome, err := f.h.HandleEmail(t.Context(), nil, email, jmap.OriginInbox)
	r.NoError(err)
	r.Equal(jmap.OutcomeIgnored, outcome)
}

func TestHandleEmailRejectsUnknownToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{}, &stubResolver{})
	email := jmap.Email{To: []jmap.EmailAddress{{Email: strings.Repeat("ab", 24) + "@inbox.example.com"}}}

	r.True(f.h.ClaimsEmail(email))

	outcome, err := f.h.HandleEmail(t.Context(), nil, email, jmap.OriginInbox)
	r.NoError(err)
	r.Equal(jmap.OutcomeRejected, outcome)
	r.Zero(f.countResults(t))
}

func TestHandleEmailDeduplicatesByMessageID(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{}, &stubResolver{})
	f.deliver(t, testToken+"@inbox.example.com", "<dup@x>")
	f.deliver(t, testToken+"@inbox.example.com", "<dup@x>")

	r.Equal(1, f.countResults(t))
}

func TestHandleEmailRescanWithoutMessageIDRecordsNothing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{}, &stubResolver{})
	email := authEmail()
	email.To = []jmap.EmailAddress{{Email: testToken + "@inbox.example.com"}}
	email.ReceivedAt = time.Now()

	outcome, err := f.h.HandleEmail(t.Context(), nil, *email, jmap.OriginRescan)
	r.NoError(err)
	r.Equal(jmap.OutcomeProcessed, outcome)
	r.Zero(f.countResults(t))

	// The same message arriving live is recorded.
	_, err = f.h.HandleEmail(t.Context(), nil, *email, jmap.OriginInbox)
	r.NoError(err)
	r.Equal(1, f.countResults(t))
}

func TestRecordResultRequiresCheck(t *testing.T) {
	t.Parallel()

	f := newAuthFixture(t, models.JSONMap{}, &stubResolver{})
	err := f.h.recordResult(t.Context(), nil, authEmail(), statusUp, jmap.OriginInbox)
	require.ErrorIs(t, err, ErrCheckMissing)
}

func TestRecordResultFailsWhenDatabaseIsUnavailable(t *testing.T) {
	t.Parallel()

	f := newAuthFixture(t, models.JSONMap{}, &stubResolver{})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	email := authEmail()
	email.MessageID = []string{"<canceled@x>"}

	err := f.h.recordResult(ctx, f.check, email, statusUp, jmap.OriginInbox)
	require.Error(t, err)
	require.Zero(t, f.countResults(t))
}

func TestToCheckerStatus(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(checkerdef.StatusUp, toCheckerStatus(statusUp))
	r.Equal(checkerdef.StatusDown, toCheckerStatus(statusDown))
	r.Equal(checkerdef.StatusError, toCheckerStatus(statusError))
	r.Equal(checkerdef.StatusRunning, toCheckerStatus(statusRunning))
	r.Equal(checkerdef.StatusUp, toCheckerStatus("unknown"))
}

// TestHandleEmailRecordsMailForTheSingleMatchingCheck is the positive control
// of spec 2026-11-14-01: a token naming exactly one live check is recorded.
func TestHandleEmailRecordsMailForTheSingleMatchingCheck(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{}, &stubResolver{})
	f.deliver(t, testToken+"@inbox.example.com", "<single@x>")

	r.Equal(1, f.countResults(t))
}

// TestHandleEmailRejectsTokenSharedByTwoChecks: with the unique index gone
// (data corruption), a token naming two live checks is rejected and recorded
// on neither, instead of landing on whichever row the database returns first.
func TestHandleEmailRejectsTokenSharedByTwoChecks(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{}, &stubResolver{})

	_, err := f.svc.DB().ExecContext(t.Context(), `drop index idx_checks_email_token_unique`)
	r.NoError(err)

	twin := models.NewCheck(f.check.OrganizationUID, "auth-check-twin", "email")
	twin.Config = models.JSONMap{"token": testToken}
	r.NoError(f.svc.CreateCheck(t.Context(), twin))

	email := authEmail()
	email.To = []jmap.EmailAddress{{Email: testToken + "@inbox.example.com"}}
	email.MessageID = []string{"<shared@x>"}
	email.ReceivedAt = time.Now()

	outcome, err := f.h.HandleEmail(t.Context(), nil, *email, jmap.OriginInbox)
	r.NoError(err)
	r.Equal(jmap.OutcomeRejected, outcome)
	r.Zero(f.countResults(t))
}
