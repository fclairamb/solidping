package statuspages

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/statuspagelock"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// Password-protection errors (spec 2026-08-21-07).
var (
	// ErrInvalidVisibility is returned for a visibility outside the three
	// supported values.
	ErrInvalidVisibility = errors.New("invalid status page visibility")
	// ErrPasswordRequired is returned when a page is switched to `password`
	// visibility without one being set (now or previously).
	ErrPasswordRequired = errors.New("a password is required for password-protected visibility")
	// ErrPasswordTooShort is returned for a password under MinPasswordLength.
	ErrPasswordTooShort = errors.New("status page password is too short")
	// ErrWrongPassword is returned by Unlock for a bad password.
	ErrWrongPassword = errors.New("wrong status page password")
	// ErrTooManyUnlockAttempts is returned when the per-IP+page unlock limiter
	// trips.
	ErrTooManyUnlockAttempts = errors.New("too many unlock attempts")
)

// MinPasswordLength is the floor for a status-page password.
//
// Deliberately lower than an account password's policy: this is a shared
// secret an operator pastes into a customer email, not a credential tied to an
// identity, and it protects a read-only page. Making it long enough to be
// annoying to share would just push operators back to `public`. Brute force is
// answered by the rate limiter below, not by length alone.
const MinPasswordLength = 6

// Unlock attempt limits, per (IP, page).
const (
	unlockAttemptsPerWindow = 10
	unlockWindow            = 5 * time.Minute
)

// validateVisibility checks a requested visibility value.
func validateVisibility(visibility *string) error {
	if visibility == nil {
		return nil
	}

	if !models.ValidStatusPageVisibility(*visibility) {
		return ErrInvalidVisibility
	}

	return nil
}

// hashPagePassword turns the write-only `password` request field into the
// value to store.
//
// The three cases are distinct on purpose:
//
//	nil          → nil   (leave the column untouched)
//	""           → ""    (CLEAR the column; db layer writes NULL)
//	"s3cret"     → hash  (set/replace, invalidating every outstanding unlock)
func hashPagePassword(password *string) (*string, error) {
	if password == nil {
		return nil, nil //nolint:nilnil // nil = "leave untouched", distinct from a value
	}

	trimmed := strings.TrimSpace(*password)
	if trimmed == "" {
		empty := ""

		return &empty, nil
	}

	if len(trimmed) < MinPasswordLength {
		return nil, ErrPasswordTooShort
	}

	hashed, err := passwords.Hash(trimmed)
	if err != nil {
		return nil, err
	}

	return &hashed, nil
}

// resolvePasswordChange validates the combination of a requested visibility
// and a requested password against what the page already stores, and returns
// the hash to write (nil = leave alone).
//
// The rule it enforces is the one that keeps a page from ever being *stuck*
// locked or *accidentally* opened:
//
//   - switching TO `password` requires a password, unless one is already
//     stored (re-saving an unrelated field must not force a re-type);
//   - clearing the password while still on `password` visibility is refused —
//     it would leave a page nobody, including the operator, can open.
func resolvePasswordChange(
	current *models.StatusPage, visibility, password *string,
) (*string, error) {
	hashed, err := hashPagePassword(password)
	if err != nil {
		return nil, err
	}

	effective := ""
	if current != nil {
		effective = current.Visibility
	}

	if visibility != nil {
		effective = *visibility
	}

	if effective != models.StatusPageVisibilityPassword {
		return hashed, nil
	}

	// Target state is `password`: something must gate it once we are done.
	settingOne := hashed != nil && *hashed != ""
	clearingOne := hashed != nil && *hashed == ""
	hasOne := current != nil && current.PasswordHash != nil && *current.PasswordHash != ""

	if settingOne || (hasOne && !clearingOne) {
		return hashed, nil
	}

	return nil, ErrPasswordRequired
}

// --- Unlock ---

// UnlockResult carries what the handler needs to set the cookie.
type UnlockResult struct {
	PageUID string
	Token   string
}

// Unlock verifies a password against a page and mints an unlock token.
//
// It answers ErrStatusPageNotFound for anything that is not a live,
// password-protected page — including a `public` one. Telling a caller "this
// page exists but needs no password" would be harmless; telling them "this
// private page exists" would not, and one branch is easier to keep correct
// than two.
func (s *Service) Unlock(
	ctx context.Context, orgSlug, slug, clientIP, password string,
) (*UnlockResult, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	// An empty slug addresses the org's DEFAULT page, exactly like the view
	// endpoint — status0 reaches a default page through /status0/<org> and has
	// no slug to send.
	var page *models.StatusPage

	if slug == "" {
		page, err = s.db.GetDefaultStatusPage(ctx, org.UID)
	} else {
		page, err = s.db.GetStatusPageBySlug(ctx, org.UID, slug)
	}

	if err != nil || page == nil || !page.Enabled || !statuspagelock.Protected(page) {
		return nil, ErrStatusPageNotFound
	}

	// Rate limit BEFORE the (deliberately expensive) hash comparison: an
	// argon2id verify is the most expensive thing on this endpoint, so an
	// unthrottled unlock is a CPU-exhaustion lever as much as a guessing one.
	if !s.unlockLimiter.allow(clientIP, page.UID) {
		return nil, ErrTooManyUnlockAttempts
	}

	if !passwords.Verify(password, *page.PasswordHash) {
		return nil, ErrWrongPassword
	}

	return &UnlockResult{
		PageUID: page.UID,
		Token:   statuspagelock.Issue(*page.PasswordHash, page.UID, time.Now(), statuspagelock.TTL),
	}, nil
}

// unlockRateLimiter throttles unlock attempts per (client IP, page).
//
// Keyed on both, not just the IP: one office NAT sharing an address must not
// lock a colleague out of a different page, and a single attacker must not be
// able to spread guesses across pages to dodge the limit.
type unlockRateLimiter struct {
	mu      sync.Mutex
	windows map[string]*unlockWindowState
}

type unlockWindowState struct {
	count int
	until time.Time
}

func newUnlockRateLimiter() *unlockRateLimiter {
	return &unlockRateLimiter{windows: make(map[string]*unlockWindowState)}
}

// allow records an attempt and reports whether it is within the window budget.
func (l *unlockRateLimiter) allow(clientIP, pageUID string) bool {
	if l == nil {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	key := clientIP + "|" + pageUID

	state, ok := l.windows[key]
	if !ok || now.After(state.until) {
		// Opportunistic sweep: this map is only ever touched by unlock
		// attempts, so it is small, and expiring siblings here avoids a
		// dedicated janitor goroutine for a handful of entries.
		l.sweep(now)

		state = &unlockWindowState{until: now.Add(unlockWindow)}
		l.windows[key] = state
	}

	state.count++

	return state.count <= unlockAttemptsPerWindow
}

// sweep drops expired windows. Caller holds the lock.
func (l *unlockRateLimiter) sweep(now time.Time) {
	for key, state := range l.windows {
		if now.After(state.until) {
			delete(l.windows, key)
		}
	}
}
