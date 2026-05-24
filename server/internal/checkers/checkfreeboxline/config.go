// Package checkfreeboxline provides Freebox xDSL/FTTH line-quality checks
// backed by the Freebox OS /api/v4/ endpoints. The check references an
// IntegrationConnection of type `freebox` by UID; at execution time the
// connection's encrypted app_token is resolved via the package-level
// ConnectionResolver indirection (wired from the API server at startup).
package checkfreeboxline

import (
	"fmt"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Link types accepted in FreeboxLineConfig.LinkType.
const (
	LinkTypeXDSL = "xdsl"
	LinkTypeFTTH = "ftth"
)

// FreeboxLineConfig holds the configuration for a Freebox line-quality
// check. The check is "connection-backed": ConnectionUID refers to an
// integration_connections row of type `freebox` that owns the encrypted
// app_token. Thresholds are simple absolute values; zero disables the
// corresponding check.
type FreeboxLineConfig struct {
	// ConnectionUID references an integration_connections row with type "freebox".
	ConnectionUID string `json:"connectionUid"`

	// LinkType forces xDSL or FTTH parsing; "xdsl" or "ftth". Required.
	LinkType string `json:"linkType"`

	// xDSL thresholds (ignored for FTTH checks). 0 = disabled.
	MinSyncRateDownKbps int `json:"minSyncRateDownKbps,omitempty"`
	MinSnrMarginDownDb  int `json:"minSnrMarginDownDb,omitempty"`
	MaxAttenuationDb    int `json:"maxAttenuationDb,omitempty"`
	MaxCrcErrorsPerRun  int `json:"maxCrcErrorsPerRun,omitempty"`

	// FTTH thresholds (ignored for xDSL checks). 0 = disabled.
	MinRxPowerMw float64 `json:"minRxPowerMw,omitempty"`
	MaxRxPowerMw float64 `json:"maxRxPowerMw,omitempty"`
}

// Config map keys — kept as constants so FromMap / GetConfig can't drift.
const (
	keyConnectionUID       = "connectionUid"
	keyLinkType            = "linkType"
	keyMinSyncRateDownKbps = "minSyncRateDownKbps"
	keyMinSnrMarginDownDb  = "minSnrMarginDownDb"
	keyMaxAttenuationDb    = "maxAttenuationDb"
	keyMaxCrcErrorsPerRun  = "maxCrcErrorsPerRun"
	keyMinRxPowerMw        = "minRxPowerMw"
	keyMaxRxPowerMw        = "maxRxPowerMw"
)

// FromMap populates the configuration from a map (typically the JSONB
// `config` column shape).
//
//nolint:cyclop // straightforward sequential field extraction
func (c *FreeboxLineConfig) FromMap(configMap map[string]any) error {
	if v, ok := configMap[keyConnectionUID].(string); ok {
		c.ConnectionUID = v
	} else if configMap[keyConnectionUID] != nil {
		return checkerdef.NewConfigError(keyConnectionUID, "must be a string")
	}

	if v, ok := configMap[keyLinkType].(string); ok {
		c.LinkType = v
	} else if configMap[keyLinkType] != nil {
		return checkerdef.NewConfigError(keyLinkType, "must be a string")
	}

	if err := extractInt(configMap, keyMinSyncRateDownKbps, &c.MinSyncRateDownKbps); err != nil {
		return err
	}

	if err := extractInt(configMap, keyMinSnrMarginDownDb, &c.MinSnrMarginDownDb); err != nil {
		return err
	}

	if err := extractInt(configMap, keyMaxAttenuationDb, &c.MaxAttenuationDb); err != nil {
		return err
	}

	if err := extractInt(configMap, keyMaxCrcErrorsPerRun, &c.MaxCrcErrorsPerRun); err != nil {
		return err
	}

	if err := extractFloat(configMap, keyMinRxPowerMw, &c.MinRxPowerMw); err != nil {
		return err
	}

	if err := extractFloat(configMap, keyMaxRxPowerMw, &c.MaxRxPowerMw); err != nil {
		return err
	}

	return nil
}

// GetConfig serializes the config back to a map. Zero-valued thresholds
// are omitted to keep the persisted JSONB tidy.
func (c *FreeboxLineConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		keyConnectionUID: c.ConnectionUID,
		keyLinkType:      c.LinkType,
	}

	if c.MinSyncRateDownKbps != 0 {
		cfg[keyMinSyncRateDownKbps] = c.MinSyncRateDownKbps
	}

	if c.MinSnrMarginDownDb != 0 {
		cfg[keyMinSnrMarginDownDb] = c.MinSnrMarginDownDb
	}

	if c.MaxAttenuationDb != 0 {
		cfg[keyMaxAttenuationDb] = c.MaxAttenuationDb
	}

	if c.MaxCrcErrorsPerRun != 0 {
		cfg[keyMaxCrcErrorsPerRun] = c.MaxCrcErrorsPerRun
	}

	if c.MinRxPowerMw != 0 {
		cfg[keyMinRxPowerMw] = c.MinRxPowerMw
	}

	if c.MaxRxPowerMw != 0 {
		cfg[keyMaxRxPowerMw] = c.MaxRxPowerMw
	}

	return cfg
}

// Validate ensures the configuration is well-formed. Performed without
// any network call — wire validation happens during Execute.
//
//nolint:cyclop // a flat threshold-by-threshold validator is the clearest form
func (c *FreeboxLineConfig) Validate() error {
	if c.ConnectionUID == "" {
		return checkerdef.NewConfigError(keyConnectionUID, "is required")
	}

	switch c.LinkType {
	case LinkTypeXDSL, LinkTypeFTTH:
	default:
		return checkerdef.NewConfigErrorf(
			keyLinkType,
			"must be %q or %q, got %q", LinkTypeXDSL, LinkTypeFTTH, c.LinkType,
		)
	}

	if c.MinSyncRateDownKbps < 0 {
		return checkerdef.NewConfigError(keyMinSyncRateDownKbps, "must be >= 0")
	}

	if c.MinSnrMarginDownDb < 0 {
		return checkerdef.NewConfigError(keyMinSnrMarginDownDb, "must be >= 0")
	}

	if c.MaxAttenuationDb < 0 {
		return checkerdef.NewConfigError(keyMaxAttenuationDb, "must be >= 0")
	}

	if c.MaxCrcErrorsPerRun < 0 {
		return checkerdef.NewConfigError(keyMaxCrcErrorsPerRun, "must be >= 0")
	}

	if c.MinRxPowerMw < 0 {
		return checkerdef.NewConfigError(keyMinRxPowerMw, "must be >= 0")
	}

	if c.MaxRxPowerMw < 0 {
		return checkerdef.NewConfigError(keyMaxRxPowerMw, "must be >= 0")
	}

	if c.MaxRxPowerMw != 0 && c.MinRxPowerMw > c.MaxRxPowerMw {
		return checkerdef.NewConfigErrorf(
			keyMinRxPowerMw,
			"minRxPowerMw (%g) must not exceed maxRxPowerMw (%g)",
			c.MinRxPowerMw, c.MaxRxPowerMw,
		)
	}

	return nil
}

// SecretFields implements credentials.SecretFielder. The check config
// itself carries no secrets — the app_token lives on the referenced
// IntegrationConnection. Returning an empty slice (rather than nil) makes
// the no-secret intent explicit.
func (c *FreeboxLineConfig) SecretFields() []string {
	return []string{}
}

// extractInt pulls an int-valued field from the config map, tolerating
// both JSON-number (float64) and direct int representations.
func extractInt(m map[string]any, key string, out *int) error {
	v, present := m[key]
	if !present || v == nil {
		return nil
	}

	switch n := v.(type) {
	case int:
		*out = n
	case int64:
		*out = int(n)
	case float64:
		*out = int(n)
	default:
		return checkerdef.NewConfigErrorf(key, "must be a number, got %T", v)
	}

	return nil
}

// extractFloat pulls a float-valued field from the config map.
func extractFloat(m map[string]any, key string, out *float64) error {
	v, present := m[key]
	if !present || v == nil {
		return nil
	}

	switch n := v.(type) {
	case float64:
		*out = n
	case float32:
		*out = float64(n)
	case int:
		*out = float64(n)
	case int64:
		*out = float64(n)
	default:
		return checkerdef.NewConfigErrorf(key, "must be a number, got %T", v)
	}

	return nil
}

// Ensure FreeboxLineConfig satisfies the Config interface at compile time.
var _ checkerdef.Config = (*FreeboxLineConfig)(nil)

// ErrResolverNotConfigured is returned when Execute is called without a
// ConnectionResolver wired up — typically a unit-test misconfiguration.
var ErrResolverNotConfigured = fmt.Errorf("freebox_line: ConnectionResolver is not set")
