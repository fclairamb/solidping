package entitlements

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// ErrCountryNotAllowed is returned when the destination's country calling code
// is outside SP_SMS_ALLOWED_COUNTRIES. Distinct from a quota error because it
// is not a volume problem — a retry a minute later fails identically.
var ErrCountryNotAllowed = errors.New("destination country is not in the instance SMS allow-list")

// CountryError carries the refused destination's country code so the caller
// can log it (and only it — never the full number).
type CountryError struct {
	// CountryCode is the matched E.164 country calling code, or the leading
	// digits (at most three — country codes are 1-3 digits, so this can never
	// leak subscriber digits) when nothing in the allow-list matched. Empty
	// when the destination could not be parsed at all.
	CountryCode string
}

func (e *CountryError) Error() string {
	if e.CountryCode == "" {
		return ErrCountryNotAllowed.Error() + ": unparseable destination"
	}

	return ErrCountryNotAllowed.Error() + ": +" + e.CountryCode
}

// Unwrap allows errors.Is(err, ErrCountryNotAllowed) to match.
func (e *CountryError) Unwrap() error { return ErrCountryNotAllowed }

// SMSGuardStatus reports the instance-spend guards and how often they have
// refused a send for this org since the process started. Both counters are
// process-local, like the runaway buckets themselves; they exist so a breach
// surfaces on the org Usage page instead of only in the logs.
type SMSGuardStatus struct {
	// GlobalRunawayPerHour is the configured instance-wide cap, 0 when
	// disabled.
	GlobalRunawayPerHour int `json:"globalRunawayPerHour"`
	// AllowedCountries is the configured allow-list of E.164 country calling
	// codes; empty means every destination is allowed.
	AllowedCountries []string `json:"allowedCountries,omitempty"`
	// GlobalRunawayBreaches counts sends this org lost to the instance-wide
	// cap since process start.
	GlobalRunawayBreaches int `json:"globalRunawayBreaches"`
	// CountryBlockedBreaches counts sends this org lost to the allow-list
	// since process start.
	CountryBlockedBreaches int `json:"countryBlockedBreaches"`
	// LastBreachAt is when either guard last refused a send for this org.
	LastBreachAt *time.Time `json:"lastBreachAt,omitempty"`
}

// Breached reports whether either guard has ever refused a send for this org.
func (s *SMSGuardStatus) Breached() bool {
	return s.GlobalRunawayBreaches > 0 || s.CountryBlockedBreaches > 0
}

// smsGuardCounters is the per-org breach tally.
type smsGuardCounters struct {
	globalRunaway  int
	countryBlocked int
	lastAt         time.Time
}

// instanceSMSGuards holds the instance-spend guard state. Zero value = no
// guards configured, which is what a deployment with no instance SMS provider
// gets.
type instanceSMSGuards struct {
	mu               sync.Mutex
	globalPerHour    int
	allowedCountries []string
	bucket           *hourlyBucket
	counters         map[string]*smsGuardCounters
}

// WithInstanceSMSGuards configures the instance-spend guards: an instance-wide
// hourly cap and a destination-country allow-list. Both apply ONLY to sends
// made on the SERVER's credentials — a bring-your-own send bills the customer's
// own account, so it must neither consume the shared cap nor be gated by an
// allow-list chosen for the instance's own bill.
//
// perHour <= 0 disables the cap. An empty allowedCountries disables the
// allow-list.
func WithInstanceSMSGuards(perHour int, allowedCountries []string) Option {
	return func(s *Service) {
		s.instanceSMS.globalPerHour = perHour
		s.instanceSMS.allowedCountries = append([]string(nil), allowedCountries...)
	}
}

// ReserveInstanceSMS applies the two INSTANCE-SPEND guards before a send made
// on the server's own credentials: the destination-country allow-list, then
// the instance-wide hourly cap. It is deliberately separate from ReserveSMS —
// the per-org guards in ReserveSMS apply to every send whoever pays, while
// these two apply only when the instance is the one paying.
//
// Callers MUST NOT call this for a bring-your-own send.
//
// Returns a *CountryError when the destination is out of the allow-list and a
// *QuotaError when the instance-wide cap is exhausted; nil when reserved.
func (s *Service) ReserveInstanceSMS(_ context.Context, orgUID, toNumber string) error {
	s.instanceSMS.mu.Lock()
	allowed := s.instanceSMS.allowedCountries
	perHour := s.instanceSMS.globalPerHour
	s.instanceSMS.mu.Unlock()

	if code, ok := countryAllowed(toNumber, allowed); !ok {
		s.recordSMSGuardBreach(orgUID, false)

		return &CountryError{CountryCode: code}
	}

	if perHour <= 0 {
		return nil
	}

	if !s.instanceRunawayBucket(perHour).allow(s.now()) {
		s.recordSMSGuardBreach(orgUID, true)

		return &QuotaError{
			LimitName:    "instance_sms_runaway_per_hour",
			Limit:        perHour,
			CurrentUsage: perHour,
		}
	}

	return nil
}

// LogInstanceSMSBreach emits the loud log a guard breach owes an operator. The
// destination number is never logged in full — only its country code — because
// a refused alert is still a subscriber's phone number.
func (s *Service) LogInstanceSMSBreach(ctx context.Context, log *slog.Logger, orgUID string, err error) {
	if log == nil || err == nil {
		return
	}

	var countryErr *CountryError
	if errors.As(err, &countryErr) {
		log.ErrorContext(ctx, "instance SMS refused: destination country not in SP_SMS_ALLOWED_COUNTRIES",
			"orgUID", orgUID, "countryCode", countryErr.CountryCode)

		return
	}

	log.ErrorContext(ctx, "instance SMS refused: instance-wide hourly cap exhausted "+
		"(SP_SMS_GLOBAL_RUNAWAY_PER_HOUR) — alerts are being dropped instance-wide",
		"orgUID", orgUID, "error", err)
}

// SMSGuardStatusFor returns the guard configuration plus this org's breach
// tally, for the Usage page. Returns nil when no instance-spend guard is
// configured at all, so the page renders nothing rather than an empty panel.
func (s *Service) SMSGuardStatusFor(orgUID string) *SMSGuardStatus {
	s.instanceSMS.mu.Lock()
	defer s.instanceSMS.mu.Unlock()

	if s.instanceSMS.globalPerHour <= 0 && len(s.instanceSMS.allowedCountries) == 0 {
		return nil
	}

	status := &SMSGuardStatus{
		GlobalRunawayPerHour: s.instanceSMS.globalPerHour,
		AllowedCountries:     append([]string(nil), s.instanceSMS.allowedCountries...),
	}

	if counters, ok := s.instanceSMS.counters[orgUID]; ok {
		status.GlobalRunawayBreaches = counters.globalRunaway
		status.CountryBlockedBreaches = counters.countryBlocked

		if !counters.lastAt.IsZero() {
			at := counters.lastAt
			status.LastBreachAt = &at
		}
	}

	return status
}

func (s *Service) recordSMSGuardBreach(orgUID string, runaway bool) {
	s.instanceSMS.mu.Lock()
	defer s.instanceSMS.mu.Unlock()

	if s.instanceSMS.counters == nil {
		s.instanceSMS.counters = make(map[string]*smsGuardCounters)
	}

	counters, ok := s.instanceSMS.counters[orgUID]
	if !ok {
		counters = &smsGuardCounters{}
		s.instanceSMS.counters[orgUID] = counters
	}

	if runaway {
		counters.globalRunaway++
	} else {
		counters.countryBlocked++
	}

	counters.lastAt = s.now()
}

func (s *Service) instanceRunawayBucket(capacity int) *hourlyBucket {
	s.instanceSMS.mu.Lock()
	defer s.instanceSMS.mu.Unlock()

	if s.instanceSMS.bucket == nil {
		s.instanceSMS.bucket = newHourlyBucket(capacity, s.now())
	}

	return s.instanceSMS.bucket
}

// countryAllowed reports whether an E.164 destination falls inside the
// allow-list, and returns the matched (or leading) country calling code for
// logging. An empty allow-list allows everything.
//
// E.164 country calling codes are prefix-free by design, so a plain
// longest-prefix match over the allow-list is unambiguous.
func countryAllowed(toNumber string, allowed []string) (string, bool) {
	if len(allowed) == 0 {
		return "", true
	}

	digits := strings.TrimPrefix(strings.TrimSpace(toNumber), "+")
	if digits == "" {
		return "", false
	}

	best := ""

	for _, code := range allowed {
		if strings.HasPrefix(digits, code) && len(code) > len(best) {
			best = code
		}
	}

	if best != "" {
		return best, true
	}

	// Nothing matched: report the first three digits as a coarse hint. Country
	// codes are 1-3 digits, so this never leaks subscriber digits for a number
	// long enough to be dialable.
	hint := digits
	if len(hint) > 3 {
		hint = hint[:3]
	}

	return hint, false
}
