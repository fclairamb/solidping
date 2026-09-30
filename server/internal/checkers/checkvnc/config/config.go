// Package config holds the vnc check's configuration: the struct, its map
// parsing and serialization, and the whole offline rule set (ValidateSpec).
//
// It is deliberately free of the protocol code the parent checkvnc package
// links, so `sp checks validate` can run the server's own validators against a
// config-as-code manifest. The parent keeps a type alias, so every call site
// reads `checkvnc.VNCConfig`.
package config

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Config field defaults and bounds.
const (
	// DefaultPort is the standard VNC display :0 port.
	DefaultPort = 5900
	// DefaultTimeout covers the whole run, including a full-screen Raw frame
	// on a large desktop when a screenshot is requested.
	DefaultTimeout = 10 * time.Second
	maxTimeout     = 60 * time.Second
	minPort        = 1
	maxPort        = 65535

	// MaxPasswordLen is the number of password bytes VNC authentication (RFB
	// security type 2) actually uses: the DES key is 8 bytes, so anything
	// longer is silently truncated by every VNC server and client.
	MaxPasswordLen = 8

	keyHost        = "host"
	keyPort        = "port"
	keyTimeout     = "timeout"
	keyPassword    = "password"
	keyRequireAuth = "requireAuth"
	keyScreenshot  = "screenshot"
)

// VNCConfig holds the configuration for a VNC (RFB, RFC 6143) check.
//
// Without a password the check performs the pre-auth handshake only: RFB
// version negotiation plus the security-types list, which is enough to prove
// an RFB server answers and to audit the offered authentication methods.
//
// With a password it also authenticates with VNC authentication (security
// type 2), reads ServerInit (desktop size and name) and, when Screenshot is
// set, captures one full frame as a PNG. The connection is always opened with
// the shared flag set, so the probe never disconnects a viewer already
// attached to the desktop.
type VNCConfig struct {
	// Host is the VNC server hostname or IP (required).
	Host string `json:"host,omitempty"`

	// Port is the TCP port to connect to (default: 5900).
	Port int `json:"port,omitempty"`

	// Timeout is the maximum time for the whole check (default 10s, max 60s).
	Timeout time.Duration `json:"timeout,omitempty"`

	// Password is the VNC password (only the first 8 bytes are used by the
	// protocol). Declared a secret via SecretFields.
	Password string `json:"password,omitempty"`

	// RequireAuth marks the check Down when the server offers security type 1
	// ("None"): an exposed VNC server anyone can attach to. Nil means the
	// default, true.
	RequireAuth *bool `json:"requireAuth,omitempty"`

	// Screenshot captures the desktop once authenticated. Requires Password.
	Screenshot bool `json:"screenshot,omitempty"`
}

// RequiresAuth reports the effective requireAuth setting (default true).
func (c *VNCConfig) RequiresAuth() bool {
	return c.RequireAuth == nil || *c.RequireAuth
}

// Authenticated reports whether the check goes past the handshake: a
// password is configured.
func (c *VNCConfig) Authenticated() bool {
	return c.Password != ""
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop // Config parsing requires handling many optional fields
func (c *VNCConfig) FromMap(configMap map[string]any) error {
	if host, ok := configMap[keyHost].(string); ok {
		c.Host = host
	} else if configMap[keyHost] != nil {
		return checkerdef.NewConfigError(keyHost, "must be a string")
	}

	if port, ok := readIntKey(configMap, keyPort); ok {
		c.Port = port
	} else if configMap[keyPort] != nil {
		return checkerdef.NewConfigError(keyPort, "must be a number")
	}

	if timeout, ok := configMap[keyTimeout].(string); ok {
		duration, err := time.ParseDuration(timeout)
		if err != nil {
			return checkerdef.NewConfigError(keyTimeout, "must be a valid duration string")
		}

		c.Timeout = duration
	} else if configMap[keyTimeout] != nil {
		return checkerdef.NewConfigError(keyTimeout, "must be a string")
	}

	if password, ok := configMap[keyPassword].(string); ok {
		c.Password = password
	} else if configMap[keyPassword] != nil {
		return checkerdef.NewConfigError(keyPassword, "must be a string")
	}

	if requireAuth, ok := configMap[keyRequireAuth].(bool); ok {
		c.RequireAuth = &requireAuth
	} else if configMap[keyRequireAuth] != nil {
		return checkerdef.NewConfigError(keyRequireAuth, "must be a boolean")
	}

	if screenshot, ok := configMap[keyScreenshot].(bool); ok {
		c.Screenshot = screenshot
	} else if configMap[keyScreenshot] != nil {
		return checkerdef.NewConfigError(keyScreenshot, "must be a boolean")
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

// GetConfig returns the configuration as a map. requireAuth is only written
// when it departs from its default (true).
func (c *VNCConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		checkerdef.OutputKeyHost: c.Host,
	}

	if c.Port != 0 {
		cfg[checkerdef.OutputKeyPort] = c.Port
	}

	if c.Timeout != 0 {
		cfg[keyTimeout] = c.Timeout.String()
	}

	if c.Password != "" {
		cfg[keyPassword] = c.Password
	}

	if !c.RequiresAuth() {
		cfg[keyRequireAuth] = false
	}

	if c.Screenshot {
		cfg[keyScreenshot] = true
	}

	return cfg
}

// SecretFields declares which top-level config keys carry secrets and must be
// encrypted at rest. Implements credentials.SecretFielder.
func (c *VNCConfig) SecretFields() []string {
	return []string{keyPassword}
}

// Validate performs config-only validation (no network). It also fills in
// defaults for the optional numeric fields so Execute can rely on them.
//
// There is deliberately no period floor for the authenticated path (unlike
// rdp): attaching to a VNC console has no logon side effects.
func (c *VNCConfig) Validate() error {
	if c.Host == "" {
		return checkerdef.NewConfigError(keyHost, "is required")
	}

	if c.Port == 0 {
		c.Port = DefaultPort
	}

	if c.Port < minPort || c.Port > maxPort {
		return checkerdef.NewConfigErrorf(keyPort, "must be between %d and %d, got %d", minPort, maxPort, c.Port)
	}

	if c.Timeout == 0 {
		c.Timeout = DefaultTimeout
	}

	if c.Timeout <= 0 || c.Timeout > maxTimeout {
		return checkerdef.NewConfigErrorf(keyTimeout, "must be > 0 and <= %s, got %s", maxTimeout, c.Timeout)
	}

	if c.Screenshot && !c.Authenticated() {
		return checkerdef.NewConfigError(keyScreenshot, "requires a password")
	}

	return nil
}
