package entitlements

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

// probeEnv is a guarded service with a movable clock and a recording notifier.
type probeEnv struct {
	svc *Service

	mu     sync.Mutex
	now    time.Time
	alerts []*SMSProbingAlert
}

func newProbeEnv(t *testing.T) *probeEnv {
	t.Helper()

	env := &probeEnv{now: time.Unix(1700000000, 0)}
	env.svc = NewService(nil, DefaultsFor(config.DeploymentModeSelfHosted), 0,
		WithInstanceSMSGuards(0, []string{"33", "1"}),
		WithSMSProbingNotifier(func(_ context.Context, alert *SMSProbingAlert) {
			env.mu.Lock()
			defer env.mu.Unlock()

			env.alerts = append(env.alerts, alert)
		}),
		WithClock(func() time.Time {
			env.mu.Lock()
			defer env.mu.Unlock()

			return env.now
		}),
	)

	return env
}

func (e *probeEnv) advance(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.now = e.now.Add(d)
}

func (e *probeEnv) alertCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()

	return len(e.alerts)
}

// TestProbingAlertFiresOnceAtThreshold: K refusals within the window raise
// exactly one alert carrying the refused prefixes, and further refusals within
// the hour raise nothing more.
func TestProbingAlertFiresOnceAtThreshold(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newProbeEnv(t)
	ctx := t.Context()

	r.ErrorIs(env.svc.ReserveInstanceSMS(ctx, "org-a", "+8801711111111"), ErrCountryNotAllowed)
	env.advance(time.Minute)
	r.ErrorIs(env.svc.ReserveInstanceSMS(ctx, "org-a", "+9981234567"), ErrCountryNotAllowed)
	r.Equal(0, env.alertCount(), "two refusals are below the threshold")
	r.False(env.svc.SMSVerificationBlocked("org-a"))

	env.advance(time.Minute)
	r.ErrorIs(env.svc.ReserveInstanceSMS(ctx, "org-a", "+8801722222222"), ErrCountryNotAllowed)
	r.Equal(1, env.alertCount())

	alert := env.alerts[0]
	r.Equal("org-a", alert.OrgUID)
	r.Equal(smsProbeRefusals, alert.Refusals)
	r.Equal([]string{"880", "998"}, alert.Prefixes, "distinct prefixes, never a full number")
	r.Equal(smsProbeWindow, alert.Window)
	r.Equal(smsProbeBlockFor, alert.BlockFor)
	r.True(env.svc.SMSVerificationBlocked("org-a"))
	r.False(env.svc.SMSVerificationBlocked("org-b"), "the flag is per org")

	// More refusals within the hour: no second alert.
	for range 5 {
		env.advance(5 * time.Minute)
		r.ErrorIs(env.svc.ReserveInstanceSMS(ctx, "org-a", "+8801733333333"), ErrCountryNotAllowed)
	}
	r.Equal(1, env.alertCount(), "at most one alert per org per hour")
}

// TestProbingRefusalsOutsideWindowDoNotAlert: refusals spread wider than the
// window never reach K together.
func TestProbingRefusalsOutsideWindowDoNotAlert(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newProbeEnv(t)

	for range 5 {
		r.ErrorIs(env.svc.ReserveInstanceSMS(t.Context(), "org-a", "+8801711111111"), ErrCountryNotAllowed)
		env.advance(6 * time.Minute)
	}

	r.Equal(0, env.alertCount())
	r.False(env.svc.SMSVerificationBlocked("org-a"))

	// Allowed destinations never count.
	for range 5 {
		r.NoError(env.svc.ReserveInstanceSMS(t.Context(), "org-b", "+33612345678"))
	}
	r.Equal(0, env.alertCount())
}

// TestProbingBlockLiftsAfterAnHour: the flag lapses after smsProbeBlockFor and
// a renewed probe alerts again.
func TestProbingBlockLiftsAfterAnHour(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newProbeEnv(t)

	for range smsProbeRefusals {
		_ = env.svc.ReserveInstanceSMS(t.Context(), "org-a", "+8801711111111")
	}
	r.True(env.svc.SMSVerificationBlocked("org-a"))

	env.advance(smsProbeBlockFor - time.Second)
	r.True(env.svc.SMSVerificationBlocked("org-a"))

	env.advance(time.Second)
	r.False(env.svc.SMSVerificationBlocked("org-a"), "the block lifts after an hour")

	for range smsProbeRefusals {
		_ = env.svc.ReserveInstanceSMS(t.Context(), "org-a", "+8801711111111")
	}
	r.Equal(2, env.alertCount(), "a renewed probe after the hour alerts again")
}

// TestProbingConcurrentRefusalsAlertOnce: concurrent refusals (-race) raise one
// alert, not one per goroutine past the threshold.
func TestProbingConcurrentRefusalsAlertOnce(t *testing.T) {
	t.Parallel()

	env := newProbeEnv(t)

	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			_ = env.svc.ReserveInstanceSMS(t.Context(), "org-a", "+8801711111111")
		})
	}
	wg.Wait()

	require.Equal(t, 1, env.alertCount())
	require.True(t, env.svc.SMSVerificationBlocked("org-a"))
}

// TestCountryRefusalLogIsWarnWithDestinationPrefix: an individual refusal is
// a WARN, and the attribute says what it holds.
func TestCountryRefusalLogIsWarnWithDestinationPrefix(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc := guardService(t, WithInstanceSMSGuards(0, []string{"33"}))

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	err := svc.ReserveInstanceSMS(t.Context(), "org-a", "+8801711111111")
	svc.LogInstanceSMSBreach(t.Context(), log, "org-a", err)

	r.Contains(buf.String(), "level=WARN")
	r.Contains(buf.String(), "destinationPrefix=880")
	r.NotContains(buf.String(), "countryCode")
	r.NotContains(buf.String(), "1711111111")
}
