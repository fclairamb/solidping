package auth

import (
	"context"
	"fmt"
	"time"
)

const (
	// keyClientIP is the pending-registration state value key holding the
	// client IP the sign-up came from, so the per-IP limit can count it.
	keyClientIP = "clientIP"

	// registrationIPWindow is how far back the per-IP limit looks: a pending
	// sign-up older than this no longer takes a slot, even though its link
	// stays valid for registrationTTL.
	registrationIPWindow = 24 * time.Hour

	// registrationIPRateKeyPrefix keys the counter that lets one client IP
	// sign up at most once per registrationIPRateWindow.
	registrationIPRateKeyPrefix = "registration_ip_rl:"
	registrationIPRateWindow    = time.Minute
)

var (
	// ErrTooManyPendingRegistrations is returned when the client IP already
	// has auth.registration_max_pending_per_ip sign-ups waiting for
	// confirmation. It wraps ErrRateLimited, so the handler answers 429.
	ErrTooManyPendingRegistrations = fmt.Errorf("%w: too many unconfirmed registrations from this address",
		ErrRateLimited)
	// ErrRegistrationTooSoon is returned when the client IP already signed up
	// in the last minute. It wraps ErrRateLimited, so the handler answers 429.
	ErrRegistrationTooSoon = fmt.Errorf("%w: a registration from this address was sent less than a minute ago",
		ErrRateLimited)
)

// checkRegistrationPerIPLimits refuses a sign-up when clientIP already signed
// up in the last minute, or already has the configured number of pending
// registrations submitted in the last 24 hours. Confirming a registration
// deletes its entry and frees the slot. A new sign-up for an address this IP
// already has pending replaces that entry rather than adding one, so it is not
// counted against itself, but it still spends the minute.
//
// The minute counter is bumped atomically by the database and comes first, so
// at most one request per IP per minute reaches the count and the write of its
// entry: a parallel burst cannot all pass the count, on any replica.
//
// The limit is read from fullCfg, not the frozen s.cfg copy: the systemconfig
// overlay applies the stored parameter after NewService. 0, or no client IP
// (an in-process caller), turns both checks off.
func (s *Service) checkRegistrationPerIPLimits(ctx context.Context, clientIP, email string) error {
	limit := s.fullCfg.Auth.RegistrationMaxPendingPerIP
	if limit <= 0 || clientIP == "" {
		return nil
	}

	limited, err := s.bumpCounter(ctx, registrationIPRateKeyPrefix+clientIP, 1, registrationIPRateWindow)
	if err != nil {
		return fmt.Errorf("failed to count registrations: %w", err)
	}

	if limited {
		return ErrRegistrationTooSoon
	}

	entries, err := s.db.ListStateEntries(ctx, nil, registrationKeyPrefix)
	if err != nil {
		return fmt.Errorf("failed to list registration entries: %w", err)
	}

	// UpdatedAt, not CreatedAt: re-submitting a sign-up rewrites its entry in
	// place (SetStateEntry upserts), and that is when it was last requested.
	since := time.Now().Add(-registrationIPWindow)
	pending := 0

	for _, entry := range entries {
		if entry.Value == nil || entry.Key == registrationKeyPrefix+email || entry.UpdatedAt.Before(since) {
			continue
		}

		if ip, _ := (*entry.Value)[keyClientIP].(string); ip == clientIP {
			pending++
		}
	}

	if pending >= limit {
		return ErrTooManyPendingRegistrations
	}

	return nil
}
