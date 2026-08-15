package entitlements

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

func guardService(t *testing.T, opts ...Option) *Service {
	t.Helper()

	svc := NewService(nil, DefaultsFor(config.DeploymentModeSelfHosted), 0, opts...)
	// Freeze the clock so the hourly bucket never refills mid-test.
	frozen := time.Unix(1700000000, 0)
	svc.now = func() time.Time { return frozen }

	return svc
}

// TestInstanceRunawayCapRefusesSends is a negative control: the cap must
// REFUSE the send, not merely count it. An over-cap send that still goes out
// costs real money.
func TestInstanceRunawayCapRefusesSends(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc := guardService(t, WithInstanceSMSGuards(2, nil))

	r.NoError(svc.ReserveInstanceSMS(t.Context(), "org-a", "+33612345678"))
	r.NoError(svc.ReserveInstanceSMS(t.Context(), "org-b", "+33612345678"))

	err := svc.ReserveInstanceSMS(t.Context(), "org-c", "+33612345678")
	r.ErrorIs(err, ErrEntitlementExceeded, "the third send must be refused")

	var quotaErr *QuotaError
	r.ErrorAs(err, &quotaErr)
	r.Equal("instance_sms_runaway_per_hour", quotaErr.LimitName)

	// The cap is INSTANCE-wide: three different orgs, each far below the
	// per-org guard, still exhaust it together. That is the hole it closes.
	status := svc.SMSGuardStatusFor("org-c")
	r.NotNil(status)
	r.Equal(1, status.GlobalRunawayBreaches)
	r.True(status.Breached())
	r.NotNil(status.LastBreachAt)

	// A different org's tally is untouched — the panel is per-org.
	r.Equal(0, svc.SMSGuardStatusFor("org-a").GlobalRunawayBreaches)
}

// TestDisallowedCountryRefusesSend is a negative control on the country
// allow-list: an out-of-list destination must be refused outright, and it must
// not consume a token from the instance-wide bucket either.
func TestDisallowedCountryRefusesSend(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc := guardService(t, WithInstanceSMSGuards(1, []string{"33", "1"}))

	err := svc.ReserveInstanceSMS(t.Context(), "org-a", "+8801711111111")
	r.ErrorIs(err, ErrCountryNotAllowed, "an out-of-list destination must be refused")

	var countryErr *CountryError
	r.ErrorAs(err, &countryErr)
	r.Equal("880", countryErr.CountryCode)
	r.NotContains(err.Error(), "1711111111", "the subscriber digits must never appear")

	status := svc.SMSGuardStatusFor("org-a")
	r.Equal(1, status.CountryBlockedBreaches)
	r.Equal(0, status.GlobalRunawayBreaches)

	// The refused send did not consume the single available token: an allowed
	// destination still goes through.
	r.NoError(svc.ReserveInstanceSMS(t.Context(), "org-a", "+33612345678"))
}

func TestCountryAllowList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		allowed []string
		to      string
		ok      bool
	}{
		{"empty list allows everything", nil, "+8801711111111", true},
		{"exact country match", []string{"33"}, "+33612345678", true},
		{"north america", []string{"1"}, "+15551234567", true},
		{"not in list", []string{"33"}, "+15551234567", false},
		{"multi-code list", []string{"33", "44", "1"}, "+447700900000", true},
		{"plus is optional in the list", []string{"+33"}, "+33612345678", true},
		{"empty destination", []string{"33"}, "", false},
		{"garbage destination", []string{"33"}, "not-a-number", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// The list is normalized by config.AllowedCountryCodes in
			// production; mimic that here so "+33" behaves as documented.
			cfg := config.SMSConfig{AllowedCountries: joinCodes(tt.allowed)}
			_, ok := countryAllowed(tt.to, cfg.AllowedCountryCodes())
			require.Equal(t, tt.ok, ok)
		})
	}
}

func joinCodes(codes []string) string {
	return strings.Join(codes, ",")
}

// TestNoGuardsConfiguredIsInert proves a deployment that configures neither
// guard is completely unaffected: every send passes and the Usage page renders
// no panel at all.
func TestNoGuardsConfiguredIsInert(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc := guardService(t)

	for range 50 {
		r.NoError(svc.ReserveInstanceSMS(t.Context(), "org-a", "+8801711111111"))
	}

	r.Nil(svc.SMSGuardStatusFor("org-a"))
}

// TestGuardsAreIndependentOfPerOrgReservation pins the scoping rule: the
// instance-spend guards live in ReserveInstanceSMS only. ReserveSMS — the
// per-org guard that applies to bring-your-own sends too — must be entirely
// unaffected by them, which is what lets the send path skip the instance
// guards for a bring-your-own send while still applying the per-org one.
func TestGuardsAreIndependentOfPerOrgReservation(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc := guardService(t, WithInstanceSMSGuards(1, []string{"33"}))

	// Exhaust the instance-wide cap.
	r.NoError(svc.ReserveInstanceSMS(t.Context(), "org-a", "+33612345678"))
	r.Error(svc.ReserveInstanceSMS(t.Context(), "org-a", "+33612345678"))

	// The per-org runaway bucket is untouched by any of that: it still has its
	// full capacity, so a bring-your-own send that skips ReserveInstanceSMS
	// proceeds normally.
	for range defaultSMSRunawayPerHour {
		r.True(svc.runawayBucketFor("org-a", "sms", defaultSMSRunawayPerHour).allow(svc.now()))
	}
}
