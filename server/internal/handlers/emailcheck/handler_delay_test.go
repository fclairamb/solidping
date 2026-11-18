package emailcheck

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jmap"
)

// deliverProbe delivers a probe mail sent `delay` before it is received. A
// zero delay and withHeaders=false delivers a mail from "any other sender".
func (f *authFixture) deliverProbe(
	t *testing.T, recipient, msgID string, delay time.Duration, withHeaders bool,
) *models.Result {
	t.Helper()

	received := time.Now().Truncate(time.Second)
	email := authEmail()
	email.To = []jmap.EmailAddress{{Email: recipient}}
	email.MessageID = []string{msgID}
	email.ReceivedAt = received
	email.Subject = "probe"

	if withHeaders {
		email.Headers = append(email.Headers,
			jmap.EmailHeader{Name: headerSourceCheckUID, Value: "smtp-check"},
			jmap.EmailHeader{Name: headerSentAt, Value: received.Add(-delay).Format(time.RFC3339)})
	}

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

func TestDeliveryDelayWithinLimitIsUpWithDuration(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{"max_delivery_delay": "10m"}, &stubResolver{})
	res := f.deliverProbe(t, testToken+"@inbox.example.com", "<in@x>", 5*time.Minute, true)

	r.Equal(int(checkerdef.StatusUp), *res.Status)
	r.InDelta(300000, *res.Duration, 1500)
}

func TestDeliveryDelayOverLimitIsDown(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{"max_delivery_delay": "10m"}, &stubResolver{})
	res := f.deliverProbe(t, testToken+"@inbox.example.com", "<late@x>", 13*time.Minute+29*time.Second, true)

	r.Equal(int(checkerdef.StatusDown), *res.Status)
	r.Equal("delivered after 13m29s, limit 10m", res.Output["message"])
	r.InDelta(809000, *res.Duration, 1500)
}

func TestDeliveryDelayWithoutLimitIsUp(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{}, &stubResolver{})
	res := f.deliverProbe(t, testToken+"@inbox.example.com", "<nolimit@x>", 13*time.Minute, true)

	r.Equal(int(checkerdef.StatusUp), *res.Status)
	r.InDelta(780000, *res.Duration, 1500)
}

func TestDeliveryDelayIgnoredWithoutSentAtHeader(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{"max_delivery_delay": "1m"}, &stubResolver{})
	res := f.deliverProbe(t, testToken+"@inbox.example.com", "<plain@x>", 0, false)

	r.Equal(int(checkerdef.StatusUp), *res.Status)
	r.InDelta(0, *res.Duration, 0)
}

func TestDeliveryDelayExplicitDownWins(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{"max_delivery_delay": "10m"}, &stubResolver{})
	res := f.deliverProbe(t, testToken+"+down@inbox.example.com", "<xdown@x>", 20*time.Minute, true)

	r.Equal(int(checkerdef.StatusDown), *res.Status)
	r.Equal("Email reported failure", res.Output["message"])
}

func TestDeliveryDelayAndAuthFailureJoinMessagesKeepAuth(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{
		"max_delivery_delay": "10m",
		"blocklists":         []any{"zen.spamhaus.org"},
	}, &stubResolver{hosts: map[string][]string{"7.113.0.203.zen.spamhaus.org": {"127.0.0.2"}}})
	res := f.deliverProbe(t, testToken+"@inbox.example.com", "<both@x>", 20*time.Minute, true)

	r.Equal(int(checkerdef.StatusDown), *res.Status)
	msg, _ := res.Output["message"].(string)
	r.Contains(msg, "listed on zen.spamhaus.org")
	r.Contains(msg, "delivered after 20m, limit 10m")
	r.Contains(res.Output, "auth")
}

// TestAllFourAuthOptionsRecordAuthBlock is the step-9 regression: a check whose
// stored config carries the four options, resolved through the production
// token lookup, records an auth block.
func TestAllFourAuthOptionsRecordAuthBlock(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{
		"require_spf": "softfail", "require_dkim": true, "require_dmarc": "pass", "check_blocklists": true,
	}, &stubResolver{})
	res := f.deliver(t, testToken+"@inbox.example.com", "<four@x>")

	r.Contains(res.Output, "auth")
}
