package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/i18n/recipient"
)

// Single-use TOTP codes (spec 2026-11-03-03, RFC 6238 §5.2): a code is valid
// for about 90 s (one period either side of now), and without a record of the
// last accepted time step the same code could open a second session or turn
// 2FA off. The last accepted step is kept per user and per secret in a global
// state entry, raised by an atomic compare-and-set.
const (
	totpPeriod = 30
	totpSkew   = 1

	// totpLastStepKeyPrefix + userUID + ":" + hashTOTPSecret(secret). Keying
	// on the secret too means a fresh secret (2FA turned off then on again)
	// starts with no record.
	totpLastStepKeyPrefix = "totp_last_step:"

	// totpLastStepTTL outlives the window in which any step at or before the
	// recorded one can still validate (the recorded step can be at most one
	// period ahead of now, and stays valid for one more after that).
	totpLastStepTTL = 5 * time.Minute
)

// totpMatchedStep returns the time step code is valid for at now, within the
// same skew totp.Validate allows, or ok=false when it matches none.
func totpMatchedStep(code, secret string, now time.Time) (int, bool) {
	opts := totp.ValidateOpts{
		Period:    totpPeriod,
		Skew:      0,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	}

	current := now.UTC().Unix() / totpPeriod

	for offset := -totpSkew; offset <= totpSkew; offset++ {
		step := current + int64(offset)

		valid, err := totp.ValidateCustom(code, secret, time.Unix(step*totpPeriod, 0).UTC(), opts)
		if err == nil && valid {
			return int(step), true
		}
	}

	return 0, false
}

// consumeTOTPCode accepts code for this user and secret at most once: it
// answers ErrInvalid2FACode for a wrong code and, with the same error, for a
// code whose time step is at or before the last accepted one. On success the
// step is recorded, atomically, so two parallel requests with one code cannot
// both pass.
func (s *Service) consumeTOTPCode(ctx context.Context, userUID, code, secret string) error {
	step, ok := totpMatchedStep(code, secret, time.Now())
	if !ok {
		return ErrInvalid2FACode
	}

	raised, err := s.db.RaiseGlobalCounter(ctx,
		totpLastStepKeyPrefix+userUID+":"+hashTOTPSecret(secret), step, totpLastStepTTL)
	if err != nil {
		return fmt.Errorf("failed to record TOTP step: %w", err)
	}

	if !raised {
		return ErrInvalid2FACode
	}

	return nil
}

// notifyTOTPDisabled emails the account owner that two-factor authentication
// was turned off, the twin of notifyTOTPEnabled: removing the second factor
// behind the owner's back must not go unnoticed. Best effort.
func (s *Service) notifyTOTPDisabled(ctx context.Context, user *models.User) {
	dashboardURL := strings.TrimRight(s.fullCfg.Server.BaseURL, "/") + config.DashboardBasePath
	securityURL := dashboardURL + "/"

	if claims, ok := ctx.Value(base.ContextKeyClaims).(*Claims); ok && claims.OrgSlug != "" {
		securityURL = fmt.Sprintf("%s/orgs/%s/account/security", dashboardURL, claims.OrgSlug)
	}

	s.enqueueEmail(ctx, "", user.Email, email.TemplateTOTPDisabled, recipient.ForUser(user),
		map[string]any{
			"DisabledAt":   time.Now().UTC(),
			keySecurityURL: securityURL,
		},
	)
}
