package models

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
)

// EntitlementsPayloadVersion is the current schema version for the
// payload column. Future shape-breaking changes bump this and add a
// branch in EntitlementsPayload.UnmarshalJSON.
const EntitlementsPayloadVersion = 1

// ErrUnknownEntitlementsPayloadVersion is returned when the payload
// JSON's version discriminator does not match a known shape. Callers
// can use errors.Is to detect it.
var ErrUnknownEntitlementsPayloadVersion = errors.New("unknown entitlements payload version")

// EntitlementLimits is the quantitative half of an entitlement set.
// nil = unlimited. JSON tags are the wire format consumed by the API.
//
// Three limits are modeled: MaxChecks (total non-internal checks an org
// may own, enforced at check creation), MaxSSOUsers (capped on
// self-hosted by default) and MaxChecksPerMinute (aggregate dispatch
// rate, capped on SaaS). Adding an optional field to the v1 JSONB
// payload is backward-compatible — absent keys unmarshal to nil
// (= unlimited), so no version bump is needed. Other extra keys
// (retention, feature flags, …) remain silently ignored on read.
type EntitlementLimits struct {
	MaxChecks          *int `json:"maxChecks,omitempty"`
	MaxSSOUsers        *int `json:"maxSsoUsers,omitempty"`
	MaxChecksPerMinute *int `json:"maxChecksPerMinute,omitempty"`
}

// EntitlementsPayload is the structured-by-OSS portion of an
// org_entitlements row, stored as JSON in the `payload` column. The
// struct itself is the schema; absent keys mean "use default" and
// extra keys are silently ignored for forward-compat. The Version
// field gates shape-migrations at unmarshal time.
type EntitlementsPayload struct {
	Version     int               `json:"version"`
	Source      EntitlementSource `json:"source,omitempty"`
	Limits      EntitlementLimits `json:"limits"`
	DisplayName *string           `json:"displayName,omitempty"`
}

// Value implements driver.Valuer so bun can write the payload as JSON
// (postgres jsonb / sqlite text) without an explicit hook.
func (p *EntitlementsPayload) Value() (driver.Value, error) {
	v := *p
	if v.Version == 0 {
		v.Version = EntitlementsPayloadVersion
	}

	data, err := json.Marshal(payloadV1(v))
	if err != nil {
		return nil, fmt.Errorf("marshal entitlements payload: %w", err)
	}

	return string(data), nil
}

// Scan implements sql.Scanner. Empty / NULL values yield a zero-valued
// payload with the current version stamped in — the resolver will fall
// back to defaults for absent fields.
func (p *EntitlementsPayload) Scan(value any) error {
	if value == nil {
		*p = EntitlementsPayload{Version: EntitlementsPayloadVersion}

		return nil
	}

	var data []byte

	switch v := value.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		*p = EntitlementsPayload{Version: EntitlementsPayloadVersion}

		return nil
	}

	if len(data) == 0 || string(data) == "{}" {
		*p = EntitlementsPayload{Version: EntitlementsPayloadVersion}

		return nil
	}

	return p.UnmarshalJSON(data)
}

// UnmarshalJSON probes the version discriminator first and dispatches
// to the matching shape-migration. v0 (rows written before the version
// field landed) is treated as v1.
func (p *EntitlementsPayload) UnmarshalJSON(data []byte) error {
	var probe struct {
		Version int `json:"version"`
	}
	_ = json.Unmarshal(data, &probe)

	switch probe.Version {
	case 0, EntitlementsPayloadVersion:
		return p.unmarshalV1(data)
	default:
		return fmt.Errorf("%w: %d", ErrUnknownEntitlementsPayloadVersion, probe.Version)
	}
}

// payloadV1 is the on-disk shape for version 1. Keeping this as a
// distinct type lets UnmarshalJSON dispatch by version without the
// outer struct's UnmarshalJSON recursing on itself.
type payloadV1 EntitlementsPayload

func (p *EntitlementsPayload) unmarshalV1(data []byte) error {
	var raw payloadV1
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("unmarshal entitlements payload v1: %w", err)
	}

	raw.Version = EntitlementsPayloadVersion
	*p = EntitlementsPayload(raw)

	return nil
}
