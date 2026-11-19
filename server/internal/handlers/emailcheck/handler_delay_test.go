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

	return f.deliverMail(t, recipient, msgID, func(email *jmap.Email) {
		if withHeaders {
			email.Headers = append(email.Headers,
				jmap.EmailHeader{Name: headerSourceCheckUID, Value: "smtp-check"},
				jmap.EmailHeader{Name: headerSentAt, Value: email.ReceivedAt.Add(-delay).Format(time.RFC3339)})
		}
	})
}

// deliverDated delivers a third-party mail (no SolidPing headers) whose Date
// header is `delay` before its reception (negative: after it).
func (f *authFixture) deliverDated(t *testing.T, msgID string, delay time.Duration) *models.Result {
	t.Helper()

	return f.deliverMail(t, testToken+"@inbox.example.com", msgID, func(email *jmap.Email) {
		date := email.ReceivedAt.Add(-delay)
		email.SentAt = &date
	})
}

// deliverMail delivers a mail received now, shaped by mutate, and returns its result.
func (f *authFixture) deliverMail(
	t *testing.T, recipient, msgID string, mutate func(*jmap.Email),
) *models.Result {
	t.Helper()

	received := time.Now().Truncate(time.Second)
	email := authEmail()
	email.To = []jmap.EmailAddress{{Email: recipient}}
	email.MessageID = []string{msgID}
	email.ReceivedAt = received
	email.Subject = "probe"
	mutate(email)

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

// TestLateMailAfterWatchTimeoutThenInTimeProbe: the watch already recorded the
// missed probe as down; the late mail is recorded down with its real latency,
// and the next in-time probe is recorded up.
func TestLateMailAfterWatchTimeoutThenInTimeProbe(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{"max_delivery_delay": "10m"}, &stubResolver{})

	// The watch's timeout row (an evaluation row written by a worker; none is
	// needed for this handler path, it only has to coexist).
	timeoutStatus := int(checkerdef.StatusDown)
	r.NoError(f.svc.SaveResultWithStatusTracking(t.Context(), &models.Result{
		UID: "watch-timeout", OrganizationUID: f.check.OrganizationUID, CheckUID: f.check.UID,
		PeriodType: models.PeriodTypeRaw, PeriodStart: time.Now().Add(-time.Minute), Status: &timeoutStatus,
		Metrics: models.JSONMap{},
		Output: models.JSONMap{
			"message": "sent at 10:00, not received after 10m", "evaluation": true,
			"deliveryTimeoutAt": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano),
		},
		CreatedAt: time.Now().Add(-time.Minute),
	}))

	late := f.deliverProbe(t, testToken+"@inbox.example.com", "<late-after-timeout@x>", 13*time.Minute, true)
	r.Equal(int(checkerdef.StatusDown), *late.Status)
	r.InDelta(780000, *late.Duration, 1500)

	inTime := f.deliverProbe(t, testToken+"@inbox.example.com", "<in-time-after@x>", 2*time.Minute, true)
	r.Equal(int(checkerdef.StatusUp), *inTime.Status)
	r.InDelta(120000, *inTime.Duration, 1500)
}

func TestDeliveryDelayWithoutLimitIsUp(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{}, &stubResolver{})
	res := f.deliverProbe(t, testToken+"@inbox.example.com", "<nolimit@x>", 13*time.Minute, true)

	r.Equal(int(checkerdef.StatusUp), *res.Status)
	r.InDelta(780000, *res.Duration, 1500)
}

// Neither X-SolidPing-Sent-At nor Date: no send time, never judged late.
func TestDeliveryDelayIgnoredWithoutAnySendTime(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{"max_delivery_delay": "1m"}, &stubResolver{})
	res := f.deliverProbe(t, testToken+"@inbox.example.com", "<plain@x>", 0, false)

	r.Equal(int(checkerdef.StatusUp), *res.Status)
	r.InDelta(0, *res.Duration, 0)
	r.NotContains(res.Output, "latencyMs")
	r.NotContains(res.Output, "sentAtSource")
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

// Send time: a third-party mail gets its latency from its Date header.
func TestDeliveryDelayFromDateHeaderOverLimitIsDown(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{"max_delivery_delay": "10m"}, &stubResolver{})
	res := f.deliverDated(t, "<dated-late@x>", 13*time.Minute+29*time.Second)

	r.Equal(int(checkerdef.StatusDown), *res.Status)
	r.Equal("delivered after 13m29s, limit 10m", res.Output["message"])
	r.Equal("date", res.Output["sentAtSource"])
	r.InDelta(809000, res.Output["latencyMs"], 1500)
	r.InDelta(809000, *res.Duration, 1500)
	r.NotContains(res.Output, "sourceCheckUid")
}

func TestDeliveryDelayFromDateHeaderWithinLimitIsUp(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{"max_delivery_delay": "10m"}, &stubResolver{})
	res := f.deliverDated(t, "<dated-in@x>", 3*time.Minute)

	r.Equal(int(checkerdef.StatusUp), *res.Status)
	r.Equal("date", res.Output["sentAtSource"])
	r.InDelta(180000, *res.Duration, 1500)
}

// With both send times present and disagreeing, X-SolidPing-Sent-At wins.
func TestDeliveryDelaySentAtHeaderWinsOverDate(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{"max_delivery_delay": "10m"}, &stubResolver{})
	res := f.deliverMail(t, testToken+"@inbox.example.com", "<both-times@x>", func(email *jmap.Email) {
		date := email.ReceivedAt.Add(-time.Hour)
		email.SentAt = &date
		email.Headers = append(email.Headers, jmap.EmailHeader{
			Name: headerSentAt, Value: email.ReceivedAt.Add(-2 * time.Minute).Format(time.RFC3339),
		})
	})

	r.Equal(int(checkerdef.StatusUp), *res.Status)
	r.Equal("header", res.Output["sentAtSource"])
	r.InDelta(120000, *res.Duration, 1500)
}

// A Date after the reception or more than 24 h before it is a clock problem:
// no latency, never judged late.
func TestDeliveryDelayImplausibleDateIsIgnored(t *testing.T) {
	t.Parallel()

	for name, delay := range map[string]time.Duration{
		"future": -5 * time.Minute,
		"stale":  48 * time.Hour,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			f := newAuthFixture(t, models.JSONMap{"max_delivery_delay": "1m"}, &stubResolver{})
			res := f.deliverDated(t, "<dated-"+name+"@x>", delay)

			r.Equal(int(checkerdef.StatusUp), *res.Status)
			r.NotContains(res.Output, "latencyMs")
			r.InDelta(0, *res.Duration, 0)
		})
	}
}

// The sent-at header alone, without the source-check header, still gives a latency.
func TestDeliveryDelaySentAtWithoutSourceCheckHeader(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newAuthFixture(t, models.JSONMap{"max_delivery_delay": "10m"}, &stubResolver{})
	res := f.deliverMail(t, testToken+"@inbox.example.com", "<no-source@x>", func(email *jmap.Email) {
		email.Headers = append(email.Headers, jmap.EmailHeader{
			Name: headerSentAt, Value: email.ReceivedAt.Add(-12 * time.Minute).Format(time.RFC3339),
		})
	})

	r.Equal(int(checkerdef.StatusDown), *res.Status)
	r.Equal("header", res.Output["sentAtSource"])
	r.InDelta(720000, *res.Duration, 1500)
	r.NotContains(res.Output, "sourceCheckUid")
}
