package checkjs

import (
	"fmt"
	"time"

	"github.com/dop251/goja"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	maxScriptSize  = 64 * 1024 // 64KB max script size
	defaultTimeout = 30 * time.Second
	maxTimeout     = 30 * time.Second
	maxEnvEntries  = 50

	fieldScript  = "script"
	fieldEnv     = "env"
	fieldSecrets = "secrets"
)

// JSConfig holds the configuration for JavaScript checks.
type JSConfig struct {
	Script  string            `json:"script"`
	Timeout time.Duration     `json:"timeout,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// Secrets is the ENCRYPTED sibling of Env: same shape, same engine
	// treatment (exposed as the `secrets` global), but declared in
	// SecretFields() so credentials.SplitConfig moves the whole map into the
	// encrypted envelope.
	//
	// It is a second map rather than a flag on Env because SecretFields() is
	// per-top-level-key: making `env` secret would encrypt every plaintext
	// parameter with it — no more diffable base URL next to a password — and
	// would silently drop `env:` from every existing export and config-as-code
	// document. See spec 2026-09-11-05.
	Secrets map[string]string `json:"secrets,omitempty"`
}

// stringMapFromConfig reads an optional map[string]string config key,
// tolerating both the already-typed map (an in-process caller) and the
// map[string]any a JSON decode produces. Shared by `env` and `secrets` so the
// two cannot drift in what they accept.
//
// An absent key yields an EMPTY map rather than nil: both are `omitempty` for
// GetConfig and for JSON, and the empty map keeps the signature free of the
// nil-value/nil-error pair.
func stringMapFromConfig(configMap map[string]any, key string) (map[string]string, error) {
	out := map[string]string{}

	raw, present := configMap[key]
	if !present || raw == nil {
		return out, nil
	}

	if typed, ok := raw.(map[string]string); ok {
		return typed, nil
	}

	anyMap, ok := raw.(map[string]any)
	if !ok {
		return nil, checkerdef.NewConfigError(key, "must be a map of string key-value pairs")
	}

	for mapKey, mapVal := range anyMap {
		strVal, ok := mapVal.(string)
		if !ok {
			return nil, checkerdef.NewConfigError(key,
				fmt.Sprintf("value for key %q must be a string", mapKey))
		}

		out[mapKey] = strVal
	}

	return out, nil
}

// FromMap populates the configuration from a map.
func (c *JSConfig) FromMap(configMap map[string]any) error {
	// Extract Script (required)
	if script, ok := configMap[fieldScript].(string); ok {
		c.Script = script
	} else if configMap[fieldScript] != nil {
		return checkerdef.NewConfigError(fieldScript, "must be a string")
	}

	// Extract Timeout (optional, duration string)
	if timeout, ok := configMap["timeout"].(string); ok {
		duration, err := time.ParseDuration(timeout)
		if err != nil {
			return checkerdef.NewConfigError("timeout", "must be a valid duration string")
		}

		c.Timeout = duration
	} else if configMap["timeout"] != nil {
		return checkerdef.NewConfigError("timeout", "must be a string")
	}

	env, err := stringMapFromConfig(configMap, fieldEnv)
	if err != nil {
		return err
	}

	c.Env = env

	secrets, err := stringMapFromConfig(configMap, fieldSecrets)
	if err != nil {
		return err
	}

	c.Secrets = secrets

	return nil
}

// GetConfig returns the configuration as a map.
func (c *JSConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		fieldScript: c.Script,
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if len(c.Env) > 0 {
		cfg[fieldEnv] = stringMapToAny(c.Env)
	}

	if len(c.Secrets) > 0 {
		cfg[fieldSecrets] = stringMapToAny(c.Secrets)
	}

	return cfg
}

// stringMapToAny widens a string map for the config map, which is JSON-shaped.
func stringMapToAny(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for key, val := range in {
		out[key] = val
	}

	return out
}

// SecretFields declares which top-level config keys carry secrets and must be
// encrypted at rest. Implements credentials.SecretFielder.
//
// Only `secrets`. `env` stays deliberately public: it is the plaintext,
// diffable half of the split (a base URL, a username), and encrypting it would
// remove every non-secret script parameter from exports and from the config
// column — see JSConfig.Secrets.
//
// No ExportRedactedFields() twin is needed: the exporter strips
// SecretFields() ∪ ExportRedactedFields(), so a declared secret field is
// already absent from every rendered document (spec 2026-09-11-02).
func (c *JSConfig) SecretFields() []string {
	return []string{fieldSecrets}
}

// Validate checks that the configuration fields are within acceptable bounds.
func (c *JSConfig) Validate() error {
	if c.Script == "" {
		return checkerdef.NewConfigError(fieldScript, "is required")
	}

	if len(c.Script) > maxScriptSize {
		return checkerdef.NewConfigErrorf(fieldScript,
			"must be at most %d bytes, got %d", maxScriptSize, len(c.Script))
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf("timeout",
			"must be > 0 and <= %s, got %s", maxTimeout, c.Timeout)
	}

	if len(c.Env) > maxEnvEntries {
		return checkerdef.NewConfigErrorf(fieldEnv,
			"must have at most %d entries, got %d", maxEnvEntries, len(c.Env))
	}

	if len(c.Secrets) > maxEnvEntries {
		return checkerdef.NewConfigErrorf(fieldSecrets,
			"must have at most %d entries, got %d", maxEnvEntries, len(c.Secrets))
	}

	// Check for JavaScript syntax errors via Goja compilation
	wrapped := "(function() {\n" + c.Script + "\n})()"
	if _, err := goja.Compile(fieldScript, wrapped, true); err != nil {
		return checkerdef.NewConfigError(fieldScript,
			"syntax error: "+err.Error())
	}

	return nil
}
