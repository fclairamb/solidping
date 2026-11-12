package emailcheck

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jmap"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

type authFixture struct {
	h     *Handler
	svc   *sqlite.Service
	check *models.Check
}

func newAuthFixture(t *testing.T, cfg models.JSONMap, res *stubResolver) *authFixture {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	svc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(svc.Initialize(ctx))
	t.Cleanup(func() { _ = svc.Close() })

	org := models.NewOrganization("auth-org", "Auth Org")
	r.NoError(svc.CreateOrganization(ctx, org))

	cfg["token"] = testToken
	check := models.NewCheck(org.UID, "auth-check", "email")
	check.Config = cfg
	r.NoError(svc.CreateCheck(ctx, check))

	jobSvc := jobsvc.NewService(svc.DB(), svc, notifier.NewLocalEventNotifier(), nil)
	h := NewHandler(svc, jobSvc, nil, nil, slog.Default(), 0)
	h.dnsResolver = res

	return &authFixture{h: h, svc: svc, check: check}
}

func (f *authFixture) deliver(t *testing.T, recipient, msgID string) *models.Result {
	t.Helper()

	email := authEmail()
	email.To = []jmap.EmailAddress{{Email: recipient}}
	email.MessageID = []string{msgID}
	email.ReceivedAt = time.Now()
	email.Subject = "probe"

	outcome, err := f.h.HandleEmail(t.Context(), nil, *email, jmap.OriginInbox)
	require.NoError(t, err)
	require.Equal(t, jmap.OutcomeProcessed, outcome)

	var res models.Result
	require.NoError(t, f.svc.DB().NewSelect().Model(&res).
		Where("check_uid = ?", f.check.UID).
		Where("json_extract(output, '$.messageId') = ?", msgID).
		Scan(t.Context()))

	return &res
}

func TestHandleEmailWithoutOptionsRecordsTodaysOutput(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{}, &stubResolver{})
	res := f.deliver(t, testToken+"@inbox.example.com", "<plain@x>")

	r.Equal(int(checkerdef.StatusUp), *res.Status)
	r.Equal("Email received", res.Output["message"])
	r.NotContains(res.Output, "auth")
	r.NotContains(res.Metrics, "blocklisted_count")
}

func TestHandleEmailListedIPRecordsDown(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{"check_blocklists": true, "blocklists": []any{"zen.spamhaus.org"}},
		&stubResolver{hosts: map[string][]string{"7.113.0.203.zen.spamhaus.org": {"127.0.0.2"}}})
	res := f.deliver(t, testToken+"@inbox.example.com", "<listed@x>")

	r.Equal(int(checkerdef.StatusDown), *res.Status)
	r.Equal("delivered, but sending IP 203.0.113.7 is listed on zen.spamhaus.org", res.Output["message"])

	auth, ok := res.Output["auth"].(map[string]any)
	r.True(ok)
	r.Equal("203.0.113.7", auth["sending_ip"])
	r.Equal([]any{"zen.spamhaus.org"}, auth["blocklisted_on"])
	r.InDelta(1, res.Metrics["blocklisted_count"], 0)
}

func TestHandleEmailCleanIPStaysUpWithAuth(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{"check_blocklists": true}, &stubResolver{})
	res := f.deliver(t, testToken+"@inbox.example.com", "<clean@x>")

	r.Equal(int(checkerdef.StatusUp), *res.Status)
	r.Contains(res.Output, "auth")
	r.InDelta(0, res.Metrics["blocklisted_count"], 0)
}

func TestHandleEmailExplicitDownIsNotOverridden(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// A listed IP would also fail, but the sender's explicit +down wins and no
	// evaluation is even attempted.
	f := newAuthFixture(t, models.JSONMap{"blocklists": []any{"zen.spamhaus.org"}},
		&stubResolver{hosts: map[string][]string{"7.113.0.203.zen.spamhaus.org": {"127.0.0.2"}}})
	res := f.deliver(t, testToken+"+down@inbox.example.com", "<down@x>")

	r.Equal(int(checkerdef.StatusDown), *res.Status)
	r.Equal("Email reported failure", res.Output["message"])
	r.NotContains(res.Output, "auth")
}
