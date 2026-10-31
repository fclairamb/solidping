package config

import (
	"os"
	"strconv"
	"strings"
)

// DefaultRegistrationMaxPendingPerIP bounds the sign-ups one client IP can
// leave waiting for email confirmation over a day. A confirmed sign-up frees
// its slot, so colleagues behind one NAT get through as long as they confirm.
const DefaultRegistrationMaxPendingPerIP = 3

// EnvRegistrationMaxPendingPerIP sets auth.registration_max_pending_per_ip.
// The key is snake_case, which koanf's env provider collapses to dots, so
// applyRegistrationLimitEnv reads it by hand.
const EnvRegistrationMaxPendingPerIP = "SP_AUTH_REGISTRATION_MAX_PENDING_PER_IP"

// applyRegistrationLimitEnv reads SP_AUTH_REGISTRATION_MAX_PENDING_PER_IP. A
// malformed or negative value is ignored and the configured one stays. In test
// run mode the limit is off unless the variable is set: the Playwright suites
// sign up many accounts from one address and leave most of them unconfirmed.
func applyRegistrationLimitEnv(cfg *AuthConfig, runMode string) {
	raw := strings.TrimSpace(os.Getenv("SP_AUTH_REGISTRATION_MAX_PENDING_PER_IP"))
	if raw == "" {
		if runMode == runModeTest {
			cfg.RegistrationMaxPendingPerIP = 0
		}

		return
	}

	if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
		cfg.RegistrationMaxPendingPerIP = n
	}
}
