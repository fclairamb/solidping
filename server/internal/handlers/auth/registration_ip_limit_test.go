package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

const (
	limitTestIP      = "198.51.100.7"
	limitTestOtherIP = "198.51.100.8"
)

// setupRegistrationLimitService returns a service that accepts any email and
// allows limit pending sign-ups per IP.
func setupRegistrationLimitService(t *testing.T, limit int) (*Service, db.Service, context.Context) {
	t.Helper()

	return setupAuthTestServiceWithJobs(t, &config.Config{
		Server: config.ServerConfig{BaseURL: "https://example.com"},
		Auth: config.AuthConfig{
			JWTSecret:                   "test-jwt-secret",
			AccessTokenExpiry:           time.Hour,
			RefreshTokenExpiry:          7 * 24 * time.Hour,
			RegistrationEmailPattern:    ".*",
			RegistrationMaxPendingPerIP: limit,
		},
	})
}

func registerFromIP(ctx context.Context, svc *Service, email, clientIP string) error {
	_, err := svc.Register(ctx, RegisterRequest{
		Name: "Limit Test", Email: email, Password: "supersecret123", ClientIP: clientIP,
	})

	return err
}

// registerAMinuteLater signs up as if the last sign-up from clientIP was more
// than a minute ago, by dropping its once-a-minute counter first.
func registerAMinuteLater(ctx context.Context, svc *Service, email, clientIP string) error {
	if _, err := svc.db.DeleteStateEntry(ctx, nil, registrationIPRateKeyPrefix+clientIP); err != nil {
		return err
	}

	return registerFromIP(ctx, svc, email, clientIP)
}

// requireNothingSent asserts that a refused sign-up left no pending entry and
// queued no confirmation email for email.
func requireNothingSent(t *testing.T, dbSvc db.Service, email string) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	entry, err := dbSvc.GetStateEntry(ctx, nil, registrationKeyPrefix+email)
	r.NoError(err)
	r.Nil(entry, "a refused sign-up must not store a pending entry")
	r.Empty(extractRegistrationConfirmToken(t, ctx, dbSvc, email), "a refused sign-up must not send an email")
}

func TestRegisterPendingPerIPLimit(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupRegistrationLimitService(t, 2)

	r.NoError(registerAMinuteLater(ctx, svc, "one@acme.com", limitTestIP))
	r.NoError(registerAMinuteLater(ctx, svc, "two@acme.com", limitTestIP))

	entry, err := dbSvc.GetStateEntry(ctx, nil, registrationKeyPrefix+"one@acme.com")
	r.NoError(err)
	r.NotNil(entry)
	r.Equal(limitTestIP, (*entry.Value)[keyClientIP], "the pending entry keeps the client IP")

	err = registerAMinuteLater(ctx, svc, "three@acme.com", limitTestIP)
	r.ErrorIs(err, ErrTooManyPendingRegistrations)
	r.ErrorIs(err, ErrRateLimited, "the handler maps it to 429 through ErrRateLimited")
	requireNothingSent(t, dbSvc, "three@acme.com")

	// Another client is not affected.
	r.NoError(registerAMinuteLater(ctx, svc, "other@acme.com", limitTestOtherIP))

	// Signing up again for an address already pending replaces its entry and
	// resends the link: it does not count against itself.
	r.NoError(registerAMinuteLater(ctx, svc, "one@acme.com", limitTestIP))

	// Confirming a registration frees its slot.
	token := extractRegistrationConfirmToken(t, ctx, dbSvc, "two@acme.com")
	r.NotEmpty(token)
	_, err = svc.ConfirmRegistration(ctx, token)
	r.NoError(err)

	r.NoError(registerAMinuteLater(ctx, svc, "three@acme.com", limitTestIP))
	r.NotEmpty(extractRegistrationConfirmToken(t, ctx, dbSvc, "three@acme.com"))
}

func TestRegisterOncePerMinutePerIP(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupRegistrationLimitService(t, 10)

	r.NoError(registerFromIP(ctx, svc, "first@acme.com", limitTestIP))

	err := registerFromIP(ctx, svc, "second@acme.com", limitTestIP)
	r.ErrorIs(err, ErrRegistrationTooSoon)
	r.ErrorIs(err, ErrRateLimited, "the handler maps it to 429 through ErrRateLimited")
	requireNothingSent(t, dbSvc, "second@acme.com")

	// Resending the link of the pending address waits out the minute too.
	r.ErrorIs(registerFromIP(ctx, svc, "first@acme.com", limitTestIP), ErrRegistrationTooSoon)

	// Another client is not affected.
	r.NoError(registerFromIP(ctx, svc, "other@acme.com", limitTestOtherIP))

	// A minute later, the same client gets through.
	r.NoError(registerAMinuteLater(ctx, svc, "second@acme.com", limitTestIP))
	r.NotEmpty(extractRegistrationConfirmToken(t, ctx, dbSvc, "second@acme.com"))
}

// An IP over a limit gets the same refusal for an address that already has an
// account, and a taken address spends the minute like a real sign-up, so
// neither limit is an oracle for which addresses are taken.
func TestRegisterPerIPLimitsHideTakenEmails(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupRegistrationLimitService(t, 1)

	const takenEmail = "taken@acme.com"

	hash, err := passwords.Hash("originalpass1")
	r.NoError(err)
	user := models.NewUser(takenEmail)
	user.PasswordHash = &hash
	r.NoError(dbSvc.CreateUser(ctx, user))

	// A taken address answers like a success, spends the minute but no slot.
	r.NoError(registerFromIP(ctx, svc, takenEmail, limitTestIP))
	r.ErrorIs(registerFromIP(ctx, svc, "free@acme.com", limitTestIP), ErrRegistrationTooSoon)
	r.NoError(registerAMinuteLater(ctx, svc, "first@acme.com", limitTestIP))

	r.ErrorIs(registerAMinuteLater(ctx, svc, takenEmail, limitTestIP), ErrTooManyPendingRegistrations)
	r.ErrorIs(registerAMinuteLater(ctx, svc, "free@acme.com", limitTestIP), ErrTooManyPendingRegistrations)
}

func TestRegisterPendingPerIPLimitWindow(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupRegistrationLimitService(t, 1)

	r.NoError(registerAMinuteLater(ctx, svc, "yesterday@acme.com", limitTestIP))
	r.ErrorIs(registerAMinuteLater(ctx, svc, "today@acme.com", limitTestIP), ErrTooManyPendingRegistrations,
		"precondition: the pending sign-up takes the only slot")

	// The pending sign-up was submitted 25 hours ago: its link is still valid
	// (3 days), but it no longer counts.
	bunner, ok := dbSvc.(interface{ DB() *bun.DB })
	r.True(ok, "the test database exposes its bun handle")
	_, err := bunner.DB().NewUpdate().Model((*models.StateEntry)(nil)).
		Set("updated_at = ?", time.Now().Add(-25*time.Hour)).
		Where("key = ?", registrationKeyPrefix+"yesterday@acme.com").
		Exec(ctx)
	r.NoError(err)

	r.NoError(registerAMinuteLater(ctx, svc, "today@acme.com", limitTestIP))
}

func TestRegisterPerIPLimitsOff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		limit    int
		clientIP string
	}{
		{name: "limit 0", limit: 0, clientIP: limitTestIP},
		{name: "no client IP", limit: 1, clientIP: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			svc, _, ctx := setupRegistrationLimitService(t, tc.limit)

			// Back to back: neither the count nor the minute applies.
			for i := range 3 {
				r.NoError(registerFromIP(ctx, svc, fmt.Sprintf("user%d@acme.com", i), tc.clientIP))
			}
		})
	}
}

// A burst of parallel sign-ups from one IP gets one through: the minute
// counter is bumped atomically by the database, so the burst cannot all pass.
func TestRegisterPerIPParallelBurst(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupRegistrationLimitService(t, 10)

	const burst = 6

	var wg sync.WaitGroup

	errs := make([]error, burst)

	for i := range burst {
		wg.Go(func() {
			errs[i] = registerFromIP(ctx, svc, fmt.Sprintf("burst%d@acme.com", i), limitTestIP)
		})
	}

	wg.Wait()

	accepted := 0

	for _, err := range errs {
		if err == nil {
			accepted++

			continue
		}

		r.ErrorIs(err, ErrRegistrationTooSoon)
	}

	r.Equal(1, accepted)

	entries, err := dbSvc.ListStateEntries(ctx, nil, registrationKeyPrefix)
	r.NoError(err)
	r.Len(entries, 1)
}

// The handler takes the client IP with the trusted-proxy rules: with no
// trusted proxy, a forged X-Forwarded-For does not get a fresh budget. The
// once-a-minute refusal carries Retry-After, the dashboard tells it apart by it.
func TestRegisterHandlerPerIPLimits(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupRegistrationLimitService(t, 1)
	handler := NewHandler(svc, &config.Config{})

	doRegister := func(email, remoteAddr, forwardedFor string) *httptest.ResponseRecorder {
		t.Helper()

		body := fmt.Sprintf(`{"name":"New User","email":%q,"password":"supersecret123"}`, email)
		httpReq := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/auth/register", strings.NewReader(body))
		httpReq.RemoteAddr = remoteAddr

		if forwardedFor != "" {
			httpReq.Header.Set("X-Forwarded-For", forwardedFor)
		}

		rec := httptest.NewRecorder()
		r.NoError(handler.Register(rec, httpReq))

		return rec
	}

	r.Equal(http.StatusOK, doRegister("first@acme.com", limitTestIP+":40000", "").Code)

	entry, err := dbSvc.GetStateEntry(ctx, nil, registrationKeyPrefix+"first@acme.com")
	r.NoError(err)
	r.NotNil(entry)
	r.Equal(limitTestIP, (*entry.Value)[keyClientIP], "the port is not part of the client IP")

	rec := doRegister("second@acme.com", limitTestIP+":40001", "203.0.113.50")
	r.Equal(http.StatusTooManyRequests, rec.Code)
	r.Contains(rec.Body.String(), base.ErrorCodeRateLimited)
	r.Equal("60", rec.Header().Get("Retry-After"))
	requireNothingSent(t, dbSvc, "second@acme.com")

	_, err = dbSvc.DeleteStateEntry(ctx, nil, registrationIPRateKeyPrefix+limitTestIP)
	r.NoError(err)

	rec = doRegister("second@acme.com", limitTestIP+":40002", "203.0.113.51")
	r.Equal(http.StatusTooManyRequests, rec.Code, "a minute later, the unconfirmed sign-up still takes the slot")
	r.Contains(rec.Body.String(), base.ErrorCodeRateLimited)
	r.Empty(rec.Header().Get("Retry-After"))
	requireNothingSent(t, dbSvc, "second@acme.com")

	// Positive control: another client gets through.
	r.Equal(http.StatusOK, doRegister("second@acme.com", limitTestOtherIP+":40000", "").Code)
}
