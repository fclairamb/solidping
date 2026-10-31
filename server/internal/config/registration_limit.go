package config

import (
	"os"
	"strconv"
	"strings"
)

// DefaultRegistrationMaxPendingPerIPSaaS bounds the sign-ups one client IP can
// leave waiting for email confirmation over a day, in SaaS mode. A confirmed
// sign-up frees its slot, so colleagues behind one NAT get through as long as
// they confirm. A self-hosted instance has no default limit.
const DefaultRegistrationMaxPendingPerIPSaaS = 10

// EnvRegistrationMaxPendingPerIP sets auth.registration_max_pending_per_ip.
// The key is snake_case, which koanf's env provider collapses to dots, so
// resolveRegistrationLimit reads it by hand.
const EnvRegistrationMaxPendingPerIP = "SP_AUTH_REGISTRATION_MAX_PENDING_PER_IP"

// registrationLimitFileKey is the config-file key of the limit.
const registrationLimitFileKey = "auth.registration_max_pending_per_ip"

// DefaultRegistrationMaxPendingPerIP is the limit when neither the environment
// nor a config file sets it: DefaultRegistrationMaxPendingPerIPSaaS in SaaS
// mode, off otherwise. Off in test run mode too, whatever the deployment mode:
// the Playwright suites sign up many accounts from one address and leave most
// of them unconfirmed.
func DefaultRegistrationMaxPendingPerIP(deploymentMode, runMode string) int {
	if deploymentMode != DeploymentModeSaaS || runMode == runModeTest {
		return 0
	}

	return DefaultRegistrationMaxPendingPerIPSaaS
}

// resolveRegistrationLimit sets cfg.Auth.RegistrationMaxPendingPerIP from
// SP_AUTH_REGISTRATION_MAX_PENDING_PER_IP, else keeps a config-file value, else
// applies the mode default. A malformed or negative variable is ignored.
func resolveRegistrationLimit(cfg *Config, setInFile bool) {
	raw := strings.TrimSpace(os.Getenv("SP_AUTH_REGISTRATION_MAX_PENDING_PER_IP"))
	if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
		cfg.Auth.RegistrationMaxPendingPerIP = n

		return
	}

	if setInFile {
		return
	}

	cfg.Auth.RegistrationMaxPendingPerIP = DefaultRegistrationMaxPendingPerIP(cfg.Deployment.Mode, cfg.RunMode)
}
