package models

import (
	"bytes"
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
// Four limits are modeled: MaxChecks (total non-internal checks an org
// may own, enforced at check creation), MaxUsers (total org members —
// capped on self-hosted by default), MaxChecksPerMinute (aggregate
// dispatch rate, capped on SaaS), and MaxDeportedAgents (active deported /
// private-location agents across all private regions, enforced at
// enrollment — see AgentCreateAllowed). Adding an optional field to the v1
// JSONB payload is backward-compatible — absent keys unmarshal to nil
// (= unlimited), so no version bump is needed.
//
// MaxUsers is decoded from either the canonical `maxUsers` key or the
// deprecated `maxSsoUsers` alias (see UnmarshalJSON) so already-deployed
// billing services and stored v1 rows that still use the old key keep
// working forever. It is always marshaled back as `maxUsers`.
type EntitlementLimits struct {
	MaxChecks          *int `json:"maxChecks,omitempty"`
	MaxUsers           *int `json:"maxUsers,omitempty"`
	MaxChecksPerMinute *int `json:"maxChecksPerMinute,omitempty"`
	// MaxDeportedAgents caps the org's active deported (private-location)
	// agents across all private regions. nil = unlimited.
	MaxDeportedAgents *int `json:"maxDeportedAgents,omitempty"`
	// MaxCustomDomains caps the org's status pages served on a customer-owned
	// domain. nil = unlimited (self-hosted default); SaaS defaults to 0 and
	// billing raises it per plan.
	MaxCustomDomains *int `json:"maxCustomDomains,omitempty"`
	// MaxSmsPerMonth / MaxCallsPerMonth cap the org's outbound SMS and voice
	// calls per UTC calendar month. nil = unlimited (self-hosted default,
	// bring-your-own Twilio); SaaS defaults to 0 and billing raises it per plan.
	MaxSmsPerMonth   *int `json:"maxSmsPerMonth,omitempty"`
	MaxCallsPerMonth *int `json:"maxCallsPerMonth,omitempty"`
	// MaxWhatsappPerMonth caps the org's outbound WhatsApp template messages
	// per UTC calendar month. nil = unlimited (self-hosted default, the
	// operator brings their own WABA); SaaS defaults to 0 and billing raises
	// it per plan.
	MaxWhatsappPerMonth *int `json:"maxWhatsappPerMonth,omitempty"`
	// MaxSlos caps the org's service-level objectives (spec 2026-08-20-01).
	// nil = unlimited (self-hosted default); SaaS defaults to 2 and billing
	// raises it per plan.
	MaxSlos *int `json:"maxSlos,omitempty"`
	// WhiteLabel is the one non-numeric entitlement: whether the org may drop
	// the "powered by SolidPing" badge from its status pages (spec
	// 2026-08-21-07). It lives here rather than in a sibling struct because
	// this IS the entitlement payload billing writes, and splitting the shape
	// in two would mean a second wire contract to rotate.
	//
	// nil means "use the deployment default" — true self-hosted (nobody should
	// have to pay to unbrand their own instance), false on the SaaS, where
	// paying is what unlocks it. Note the asymmetry with the *int fields: nil
	// there means UNLIMITED, here it means DEFAULT, because a boolean has no
	// "unbounded" reading.
	WhiteLabel *bool `json:"whiteLabel,omitempty"`
}

// ErrConflictingUserLimitKeys is returned when a payload sends both the
// canonical maxUsers key and its deprecated maxSsoUsers alias at once.
var ErrConflictingUserLimitKeys = errors.New(
	"maxUsers and maxSsoUsers are mutually exclusive; send only maxUsers",
)

// UnmarshalJSON decodes the limits object, accepting `maxSsoUsers` as a
// deprecated decode-only alias for `maxUsers`. Sending both is an error.
// Unknown limit keys are still rejected so typos surface loudly, matching
// the DisallowUnknownFields contract the PUT decoder used before this
// type grew a custom unmarshaler.
func (l *EntitlementLimits) UnmarshalJSON(data []byte) error {
	// Local wire shape: same fields plus the deprecated alias. Decoded
	// strictly so unknown keys are rejected (loud typos).
	var wire struct {
		MaxChecks           *int  `json:"maxChecks"`
		MaxUsers            *int  `json:"maxUsers"`
		MaxSSOUsers         *int  `json:"maxSsoUsers"` // deprecated alias for maxUsers
		MaxChecksPerMinute  *int  `json:"maxChecksPerMinute"`
		MaxDeportedAgents   *int  `json:"maxDeportedAgents"`
		MaxCustomDomains    *int  `json:"maxCustomDomains"`
		MaxSmsPerMonth      *int  `json:"maxSmsPerMonth"`
		MaxCallsPerMonth    *int  `json:"maxCallsPerMonth"`
		MaxWhatsappPerMonth *int  `json:"maxWhatsappPerMonth"`
		MaxSlos             *int  `json:"maxSlos"`
		WhiteLabel          *bool `json:"whiteLabel"`
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return err
	}

	if wire.MaxUsers != nil && wire.MaxSSOUsers != nil {
		return ErrConflictingUserLimitKeys
	}

	l.MaxChecks = wire.MaxChecks
	l.MaxChecksPerMinute = wire.MaxChecksPerMinute
	l.MaxDeportedAgents = wire.MaxDeportedAgents
	l.MaxCustomDomains = wire.MaxCustomDomains
	l.MaxSmsPerMonth = wire.MaxSmsPerMonth
	l.MaxCallsPerMonth = wire.MaxCallsPerMonth
	l.MaxWhatsappPerMonth = wire.MaxWhatsappPerMonth
	l.MaxSlos = wire.MaxSlos
	l.WhiteLabel = wire.WhiteLabel

	if wire.MaxUsers != nil {
		l.MaxUsers = wire.MaxUsers
	} else {
		l.MaxUsers = wire.MaxSSOUsers
	}

	return nil
}

// EntitlementsPayload is the structured-by-OSS portion of an
// org_entitlements row, stored as JSON in the `payload` column. The
// struct itself is the schema; absent keys mean "use default" and
// extra keys are silently ignored for forward-compat. The Version
// field gates shape-migrations at unmarshal time.
type EntitlementsPayload struct {
	Version int               `json:"version"`
	Source  EntitlementSource `json:"source,omitempty"`
	Limits  EntitlementLimits `json:"limits"`
	// DisplayName / DisplayEmoji are billing-supplied plan identity, shown
	// in the dashboard (e.g. "🚀 Team"). Display-only — never enforced.
	DisplayName  *string `json:"displayName,omitempty"`
	DisplayEmoji *string `json:"displayEmoji,omitempty"`
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
