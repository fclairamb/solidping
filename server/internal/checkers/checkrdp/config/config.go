// Package config holds the rdp check's configuration: the struct, its
// map parsing and serialization, its key constants and the whole offline rule
// set (ValidateSpec).
//
// It is deliberately free of the execution client the parent checkrdp package
// links, so `sp checks validate` can run the server's own validators against a
// config-as-code manifest without carrying a protocol driver. The parent keeps a
// type alias, so every existing call site is unaffected.
package config

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Config field defaults and bounds.
const (
	DefaultPort    = 3389
	DefaultTimeout = 5 * time.Second
	maxTimeout     = 30 * time.Second
	// AuthMaxTimeout raises the timeout ceiling for an AUTHENTICATED run: a
	// Windows logon with profile load routinely takes 10-30 s, so the pre-auth
	// 30 s cap would fail healthy servers. 90 s max, 45 s default.
	AuthMaxTimeout = 90 * time.Second
	// AuthDefaultTimeout is the timeout an authenticated run gets when none
	// was configured.
	AuthDefaultTimeout = 45 * time.Second
	minPort            = 1
	maxPort            = 65535
	maxThresholdDays   = 3650 // 10 years — generous sanity cap, mirrors checkssl

	// EndSessionLogoff ends the session on the server: it is gone from
	// `query session` when the check returns. The default.
	EndSessionLogoff = "logoff"
	// EndSessionDisconnect leaves the session running on the server until its
	// idle policy ends it, so the next connect reattaches to it.
	EndSessionDisconnect = "disconnect"

	// AuthenticatedMinPeriod is the period floor for every authenticated run.
	// Each one is a real interactive Windows logon: it loads a user profile,
	// runs logon scripts/GPOs, may consume an RDS client access license, can
	// disconnect a real logged-in user on a single-session server, and shows
	// up in the Security event log (4624/4634) every run. Keeping the interval
	// long is the operator-facing mitigation, and the floor enforces it.
	AuthenticatedMinPeriod = 15 * time.Minute
)

// RDPConfig holds the configuration for an RDP check.
//
// With no credentials it is the pre-auth liveness verdict it has always been:
// TCP connect plus a valid X.224 Connection Confirm, plus the optional
// RequireNLA gate and the SSL-style WarningDays/CriticalDays certificate
// thresholds. No session is created and nothing is authenticated.
//
// With credentials set (Username + Password), the check performs a REAL
// interactive logon: after the handshake it runs CredSSP/NLA, finishes the
// connection sequence, waits for the screen to settle, optionally captures a
// PNG, then ends the session per EndSession. See the caveats on
// AuthenticatedMinPeriod — this is a user-visible Windows logon, not a probe.
type RDPConfig struct {
	// Host is the RDP server hostname or IP (required).
	Host string `json:"host,omitempty"`

	// Port is the TCP port to connect to (default: 3389).
	Port int `json:"port,omitempty"`

	// Timeout is the maximum time for the whole check (default: 5s pre-auth,
	// 45s with credentials; max 30s pre-auth, 90s authenticated).
	Timeout time.Duration `json:"timeout,omitempty"`

	// RequireNLA marks the check Down unless the server selects an NLA
	// (CredSSP) protocol — HYBRID or HYBRID_EX. Off by default.
	RequireNLA bool `json:"require_nla,omitempty"` //nolint:tagliatelle // API uses snake_case

	// WarningDays marks the check Warning when the server certificate expires
	// in at most this many days (evaluated only when a TLS-based protocol was
	// negotiated). 0 = disabled. Must be >= CriticalDays when both are set.
	WarningDays int `json:"warning_days,omitempty"` //nolint:tagliatelle // API uses snake_case

	// CriticalDays marks the check Down when the server certificate expires
	// in at most this many days. 0 = disabled (an already-expired certificate
	// is always Down).
	CriticalDays int `json:"critical_days,omitempty"` //nolint:tagliatelle // API uses snake_case

	// Username is the account to log on with. Setting it (together with
	// Password) turns the check into an authenticated run. Domain accounts use
	// `domain\user` or the separate Domain field.
	Username string `json:"username,omitempty"`

	// Password is the account's password. Declared a secret via SecretFields.
	Password string `json:"password,omitempty"`

	// Domain is the Windows domain for the logon. Optional: use it when the
	// account is a domain account and you prefer not to bake DOMAIN\ into
	// Username. Local accounts leave it empty.
	Domain string `json:"domain,omitempty"`

	// Screenshot captures the desktop once the logon settles. Authenticated
	// runs only; ignored pre-auth.
	Screenshot bool `json:"screenshot,omitempty"`

	// EndSession selects how an authenticated session ends:
	// "logoff" (default) logs the session off on the server — nothing left in
	// `query session`; "disconnect" leaves it running until the server's idle
	// policy ends it, so the next connect reattaches to it.
	EndSession string `json:"end_session,omitempty"` //nolint:tagliatelle // API uses snake_case
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop // Config parsing requires handling many optional fields
func (c *RDPConfig) FromMap(configMap map[string]any) error {
	if host, ok := configMap["host"].(string); ok {
		c.Host = host
	} else if configMap["host"] != nil {
		return checkerdef.NewConfigError("host", "must be a string")
	}

	if port, ok := readIntKey(configMap, "port"); ok {
		c.Port = port
	} else if configMap["port"] != nil {
		return checkerdef.NewConfigError("port", "must be a number")
	}

	if timeout, ok := configMap["timeout"].(string); ok {
		duration, err := time.ParseDuration(timeout)
		if err != nil {
			return checkerdef.NewConfigError("timeout", "must be a valid duration string")
		}

		c.Timeout = duration
	} else if configMap["timeout"] != nil {
		return checkerdef.NewConfigError("timeout", "must be a string")
	}

	if requireNLA, ok := configMap["require_nla"].(bool); ok {
		c.RequireNLA = requireNLA
	} else if configMap["require_nla"] != nil {
		return checkerdef.NewConfigError("require_nla", "must be a boolean")
	}

	if username, ok := configMap["username"].(string); ok {
		c.Username = username
	} else if configMap["username"] != nil {
		return checkerdef.NewConfigError("username", "must be a string")
	}

	if password, ok := configMap["password"].(string); ok {
		c.Password = password
	} else if configMap["password"] != nil {
		return checkerdef.NewConfigError("password", "must be a string")
	}

	if domain, ok := configMap["domain"].(string); ok {
		c.Domain = domain
	} else if configMap["domain"] != nil {
		return checkerdef.NewConfigError("domain", "must be a string")
	}

	if screenshot, ok := configMap["screenshot"].(bool); ok {
		c.Screenshot = screenshot
	} else if configMap["screenshot"] != nil {
		return checkerdef.NewConfigError("screenshot", "must be a boolean")
	}

	if endSession, ok := configMap["end_session"].(string); ok {
		c.EndSession = endSession
	} else if configMap["end_session"] != nil {
		return checkerdef.NewConfigError("end_session", "must be a string")
	}

	return c.readThresholds(configMap)
}

// readThresholds reads the optional certificate-expiry threshold fields.
func (c *RDPConfig) readThresholds(configMap map[string]any) error {
	if v, ok := readIntKey(configMap, "warning_days"); ok {
		c.WarningDays = v
	} else if configMap["warning_days"] != nil {
		return checkerdef.NewConfigError("warning_days", "must be a number")
	}

	if v, ok := readIntKey(configMap, "critical_days"); ok {
		c.CriticalDays = v
	} else if configMap["critical_days"] != nil {
		return checkerdef.NewConfigError("critical_days", "must be a number")
	}

	return nil
}

// readIntKey returns the integer value at key, accepting int or float64
// (JSON numbers decode to float64).
func readIntKey(configMap map[string]any, key string) (int, bool) {
	switch v := configMap[key].(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

// GetConfig returns the configuration as a map.
func (c *RDPConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		checkerdef.OutputKeyHost: c.Host,
	}

	if c.Port != 0 {
		cfg[checkerdef.OutputKeyPort] = c.Port
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if c.RequireNLA {
		cfg["require_nla"] = true
	}

	if c.Username != "" {
		cfg["username"] = c.Username
	}

	if c.Password != "" {
		cfg["password"] = c.Password
	}

	if c.Domain != "" {
		cfg["domain"] = c.Domain
	}

	if c.Screenshot {
		cfg["screenshot"] = true
	}

	if c.EndSession != "" && c.EndSession != EndSessionLogoff {
		cfg["end_session"] = c.EndSession
	}

	if c.WarningDays != 0 {
		cfg["warning_days"] = c.WarningDays
	}

	if c.CriticalDays != 0 {
		cfg["critical_days"] = c.CriticalDays
	}

	return cfg
}

// SecretFields declares which top-level config keys carry secrets and must be
// encrypted at rest. Implements credentials.SecretFielder.
func (c *RDPConfig) SecretFields() []string {
	return []string{"password"}
}

// Authenticated reports whether this check performs a real interactive logon:
// credentials are set. Everything else about the run (timeout ceiling and
// default, period floor, screenshot support) keys off this.
func (c *RDPConfig) Authenticated() bool {
	return c.Username != "" && c.Password != ""
}

// MinPeriodHint raises this check's period floor to 15 minutes when the check
// performs an authenticated logon. Implements checkerdef.MinPeriodHint.
//
// Every authenticated run is a real interactive Windows logon: it loads a user
// profile, runs logon scripts/GPOs, may consume an RDS client access license,
// can disconnect a real logged-in user on a single-session server, and shows
// up in the Security event log (4624/4634) every run. Running it every minute
// is exactly the mistake the caveats warn against, so the floor enforces the
// mitigation instead of trusting the operator to have read the help text. The
// pre-auth check keeps the global floor: it is a cheap handshake.
func (c *RDPConfig) MinPeriodHint() time.Duration {
	if !c.Authenticated() {
		return 0
	}

	return AuthenticatedMinPeriod
}

// Validate performs config-only validation (no network). It also fills in
// defaults for the optional numeric fields so Execute can rely on them.
//
//nolint:cyclop // SSH-style config validation has many interdependent fields
func (c *RDPConfig) Validate() error {
	if c.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	if c.Port == 0 {
		c.Port = DefaultPort
	}

	if c.Port < minPort || c.Port > maxPort {
		return checkerdef.NewConfigErrorf("port", "must be between %d and %d, got %d", minPort, maxPort, c.Port)
	}

	// Credentials pairing: username and password go together.
	if c.Username != "" && c.Password == "" {
		return checkerdef.NewConfigError("password", "is required when username is set")
	}

	if c.Password != "" && c.Username == "" {
		return checkerdef.NewConfigError("username", "is required when password is set")
	}

	// Timeout bound and default depend on whether this is an authenticated
	// run: a Windows logon with profile load routinely takes 10-30 s, so the
	// pre-auth 30 s ceiling would fail healthy servers.
	timeoutMax := maxTimeout
	timeoutDefault := DefaultTimeout
	if c.Authenticated() {
		timeoutMax = AuthMaxTimeout
		timeoutDefault = AuthDefaultTimeout
	}

	if c.Timeout == 0 {
		c.Timeout = timeoutDefault
	}

	if c.Timeout <= 0 || c.Timeout > timeoutMax {
		return checkerdef.NewConfigErrorf(
			"timeout", "must be > 0 and <= %s for this check, got %s", timeoutMax, c.Timeout)
	}

	if c.EndSession != "" && c.EndSession != EndSessionLogoff && c.EndSession != EndSessionDisconnect {
		return checkerdef.NewConfigErrorf(
			"end_session", "must be %q or %q, got %q", EndSessionLogoff, EndSessionDisconnect, c.EndSession)
	}

	if c.Screenshot && !c.Authenticated() {
		return checkerdef.NewConfigError("screenshot", "requires username and password (authenticated logon)")
	}

	return c.validateThresholds()
}

// validateThresholds enforces non-negative thresholds, a sanity cap, and the
// warning >= critical ordering when both tiers are explicitly set (same rule
// as checkssl: the warning band sits above the critical band).
func (c *RDPConfig) validateThresholds() error {
	if c.WarningDays < 0 {
		return checkerdef.NewConfigError("warning_days", "must be >= 0")
	}

	if c.CriticalDays < 0 {
		return checkerdef.NewConfigError("critical_days", "must be >= 0")
	}

	if c.WarningDays > maxThresholdDays {
		return checkerdef.NewConfigErrorf("warning_days", "must be <= %d", maxThresholdDays)
	}

	if c.CriticalDays > maxThresholdDays {
		return checkerdef.NewConfigErrorf("critical_days", "must be <= %d", maxThresholdDays)
	}

	if c.WarningDays != 0 && c.CriticalDays != 0 && c.WarningDays < c.CriticalDays {
		return checkerdef.NewConfigErrorf(
			"warning_days", "must be >= critical_days (%d), got %d", c.CriticalDays, c.WarningDays,
		)
	}

	return nil
}
