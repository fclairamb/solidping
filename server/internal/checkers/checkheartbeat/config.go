package checkheartbeat

import "github.com/fclairamb/solidping/server/internal/checkers/checkerdef"

// ConfigKeyRequireHMAC is the per-check switch that refuses unsigned beats on
// the embedded push transports. snake_case like the other checker config keys
// (sleep_ms, …).
const ConfigKeyRequireHMAC = "require_hmac"

// HeartbeatConfig holds the configuration for heartbeat checks.
type HeartbeatConfig struct {
	Token string `json:"token,omitempty"`
	// RequireHMAC refuses the plaintext-token beat form (SP1) on the embedded
	// TCP/UDP transports: only the HMAC-signed SP2 form and the HTTPS ingest
	// are accepted (spec 2026-09-01-06). Default false, matching today's
	// security posture.
	//
	// Enforcement is per check and strict, because a check that EVER accepts
	// SP1 leaks its token to a passive sniffer — and that token is also the
	// SP2 signing key. Turning this on should therefore be paired with a token
	// rotation; both the dashboard toggle and the docs say so.
	RequireHMAC bool `json:"require_hmac,omitempty"` //nolint:tagliatelle // checker config keys are snake_case
}

// FromMap populates the configuration from a map.
func (c *HeartbeatConfig) FromMap(configMap map[string]any) error {
	if token, ok := configMap["token"].(string); ok {
		c.Token = token
	} else if configMap["token"] != nil {
		return checkerdef.NewConfigError("token", "must be a string")
	}

	requireHMAC, err := RequireHMACFromConfig(configMap)
	if err != nil {
		return err
	}

	c.RequireHMAC = requireHMAC

	return nil
}

// RequireHMACFromConfig reads the require_hmac flag out of a raw check config.
//
// Exported because the push listeners read the flag straight off the stored
// `checks.config` jsonb — they must never depend on a typed decode of a config
// that a future key could make fail, since an unrelated decode error would
// silently turn a strict check permissive.
//
// A missing key is false. A present-but-not-boolean value is an error rather
// than a fallback to false: silently downgrading a security setting because it
// was mistyped is the failure mode this rejects.
func RequireHMACFromConfig(configMap map[string]any) (bool, error) {
	raw, present := configMap[ConfigKeyRequireHMAC]
	if !present || raw == nil {
		return false, nil
	}

	value, ok := raw.(bool)
	if !ok {
		return false, checkerdef.NewConfigError(ConfigKeyRequireHMAC, "must be a boolean")
	}

	return value, nil
}

// GetConfig returns the configuration as a map.
func (c *HeartbeatConfig) GetConfig() map[string]any {
	cfg := make(map[string]any)

	if c.Token != "" {
		cfg["token"] = c.Token
	}

	if c.RequireHMAC {
		cfg[ConfigKeyRequireHMAC] = true
	}

	return cfg
}
