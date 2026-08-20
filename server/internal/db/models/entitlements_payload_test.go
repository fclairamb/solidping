package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEntitlementLimits_MaxUsersCanonical decodes the canonical maxUsers key.
func TestEntitlementLimits_MaxUsersCanonical(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var l EntitlementLimits
	r.NoError(json.Unmarshal([]byte(`{"maxUsers":12}`), &l))
	r.NotNil(l.MaxUsers)
	r.Equal(12, *l.MaxUsers)
}

// TestEntitlementLimits_MaxSsoUsersAlias decodes the deprecated maxSsoUsers
// alias onto MaxUsers. Stored v1 rows are never rewritten, so this must work
// forever.
func TestEntitlementLimits_MaxSsoUsersAlias(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var l EntitlementLimits
	r.NoError(json.Unmarshal([]byte(`{"maxSsoUsers":7}`), &l))
	r.NotNil(l.MaxUsers)
	r.Equal(7, *l.MaxUsers)
}

// TestEntitlementLimits_BothKeysRejected verifies that sending both the
// canonical key and its deprecated alias is an error (400 at the handler).
func TestEntitlementLimits_BothKeysRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var l EntitlementLimits
	err := json.Unmarshal([]byte(`{"maxUsers":5,"maxSsoUsers":5}`), &l)
	r.Error(err)
	r.ErrorIs(err, ErrConflictingUserLimitKeys)
}

// TestEntitlementLimits_UnknownKeyRejected verifies typos in limit keys still
// surface loudly, preserving the DisallowUnknownFields contract.
func TestEntitlementLimits_UnknownKeyRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var l EntitlementLimits
	err := json.Unmarshal([]byte(`{"maxUserz":5}`), &l)
	r.Error(err)
}

// TestEntitlementLimits_MarshalUsesMaxUsers verifies output always uses the
// canonical key, never the deprecated alias.
func TestEntitlementLimits_MarshalUsesMaxUsers(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	limit := 9
	data, err := json.Marshal(EntitlementLimits{MaxUsers: &limit})
	r.NoError(err)
	r.Contains(string(data), `"maxUsers":9`)
	r.NotContains(string(data), "maxSsoUsers")
}

// TestEntitlementsPayload_ScanAliasRow verifies a stored v1 payload carrying
// the legacy maxSsoUsers key scans into MaxUsers via the Scanner path.
func TestEntitlementsPayload_ScanAliasRow(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var p EntitlementsPayload
	r.NoError(p.Scan(`{"version":1,"limits":{"maxSsoUsers":30}}`))
	r.NotNil(p.Limits.MaxUsers)
	r.Equal(30, *p.Limits.MaxUsers)
}

// TestEntitlementsPayload_ValueRoundTripAlias verifies that a payload decoded
// from the legacy alias re-marshals through Value() with the canonical key.
func TestEntitlementsPayload_ValueRoundTripAlias(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var p EntitlementsPayload
	r.NoError(p.Scan(`{"version":1,"limits":{"maxSsoUsers":15}}`))

	v, err := p.Value()
	r.NoError(err)
	s, ok := v.(string)
	r.True(ok)
	r.Contains(s, `"maxUsers":15`)
	r.NotContains(s, "maxSsoUsers")
}

// TestEntitlementLimits_MaxDeportedAgentsDecodes verifies the new field
// decodes onto its plain (non-aliased) wire key.
func TestEntitlementLimits_MaxDeportedAgentsDecodes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var l EntitlementLimits
	r.NoError(json.Unmarshal([]byte(`{"maxDeportedAgents":3}`), &l))
	r.NotNil(l.MaxDeportedAgents)
	r.Equal(3, *l.MaxDeportedAgents)
}

// TestEntitlementLimits_MaxDeportedAgentsAbsentIsNil verifies an absent key
// unmarshals to nil (= unlimited), the documented forward-compat contract.
func TestEntitlementLimits_MaxDeportedAgentsAbsentIsNil(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var l EntitlementLimits
	r.NoError(json.Unmarshal([]byte(`{"maxChecks":10}`), &l))
	r.Nil(l.MaxDeportedAgents)
}

// TestEntitlementLimits_MaxDeportedAgentsMarshal verifies the field
// round-trips through Marshal using its plain wire key.
func TestEntitlementLimits_MaxDeportedAgentsMarshal(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	limit := 6
	data, err := json.Marshal(EntitlementLimits{MaxDeportedAgents: &limit})
	r.NoError(err)
	r.Contains(string(data), `"maxDeportedAgents":6`)
}

// TestEntitlementsPayload_ScanMaxDeportedAgents verifies a stored v1 payload
// carrying maxDeportedAgents scans and round-trips through Value().
func TestEntitlementsPayload_ScanMaxDeportedAgents(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var p EntitlementsPayload
	r.NoError(p.Scan(`{"version":1,"limits":{"maxDeportedAgents":9}}`))
	r.NotNil(p.Limits.MaxDeportedAgents)
	r.Equal(9, *p.Limits.MaxDeportedAgents)

	v, err := p.Value()
	r.NoError(err)
	s, ok := v.(string)
	r.True(ok)
	r.Contains(s, `"maxDeportedAgents":9`)
}

// TestEntitlementLimits_MonthlyMessagingDecodes verifies the new SMS/voice
// monthly caps decode through the strict UnmarshalJSON.
func TestEntitlementLimits_MonthlyMessagingDecodes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var l EntitlementLimits
	r.NoError(json.Unmarshal([]byte(`{"maxSmsPerMonth":500,"maxCallsPerMonth":50}`), &l))
	r.NotNil(l.MaxSmsPerMonth)
	r.Equal(500, *l.MaxSmsPerMonth)
	r.NotNil(l.MaxCallsPerMonth)
	r.Equal(50, *l.MaxCallsPerMonth)
}

// TestEntitlementLimits_MonthlyMessagingAbsentIsNil verifies absent keys stay
// nil (= unlimited).
func TestEntitlementLimits_MonthlyMessagingAbsentIsNil(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var l EntitlementLimits
	r.NoError(json.Unmarshal([]byte(`{"maxChecks":10}`), &l))
	r.Nil(l.MaxSmsPerMonth)
	r.Nil(l.MaxCallsPerMonth)
}

// TestEntitlementLimits_MonthlyMessagingRoundTrip verifies Marshal uses the
// documented wire keys.
func TestEntitlementLimits_MonthlyMessagingRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sms, calls := 100, 20
	data, err := json.Marshal(EntitlementLimits{MaxSmsPerMonth: &sms, MaxCallsPerMonth: &calls})
	r.NoError(err)
	r.Contains(string(data), `"maxSmsPerMonth":100`)
	r.Contains(string(data), `"maxCallsPerMonth":20`)
}

// TestEntitlementLimits_WhatsAppWire proves maxWhatsappPerMonth survives the
// strict decoder (which rejects unknown keys) and the encoder round-trip.
func TestEntitlementLimits_WhatsAppWire(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var l EntitlementLimits
	r.NoError(json.Unmarshal([]byte(`{"maxWhatsappPerMonth":250}`), &l))
	r.NotNil(l.MaxWhatsappPerMonth)
	r.Equal(250, *l.MaxWhatsappPerMonth)

	// Absent stays nil (unlimited), not zero (nothing allowed) — the
	// difference decides whether a self-hosted org can page at all.
	var empty EntitlementLimits
	r.NoError(json.Unmarshal([]byte(`{}`), &empty))
	r.Nil(empty.MaxWhatsappPerMonth)

	// An explicit zero is a real cap, distinct from absent.
	var zero EntitlementLimits
	r.NoError(json.Unmarshal([]byte(`{"maxWhatsappPerMonth":0}`), &zero))
	r.NotNil(zero.MaxWhatsappPerMonth)
	r.Equal(0, *zero.MaxWhatsappPerMonth)

	wa := 42
	data, err := json.Marshal(EntitlementLimits{MaxWhatsappPerMonth: &wa})
	r.NoError(err)
	r.Contains(string(data), `"maxWhatsappPerMonth":42`)
}

// TestEntitlementLimits_SloWire proves maxSlos survives the strict decoder
// (which rejects unknown keys — a struct-only addition would make the API
// REJECT the key rather than ignore it) and the encoder round-trip.
func TestEntitlementLimits_SloWire(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var l EntitlementLimits
	r.NoError(json.Unmarshal([]byte(`{"maxSlos":2}`), &l))
	r.NotNil(l.MaxSlos)
	r.Equal(2, *l.MaxSlos)

	// Absent stays nil (unlimited), not zero (no objectives at all).
	var empty EntitlementLimits
	r.NoError(json.Unmarshal([]byte(`{}`), &empty))
	r.Nil(empty.MaxSlos)

	// An explicit zero is a real cap, distinct from absent.
	var zero EntitlementLimits
	r.NoError(json.Unmarshal([]byte(`{"maxSlos":0}`), &zero))
	r.NotNil(zero.MaxSlos)
	r.Equal(0, *zero.MaxSlos)

	limit := 7
	data, err := json.Marshal(EntitlementLimits{MaxSlos: &limit})
	r.NoError(err)
	r.Contains(string(data), `"maxSlos":7`)

	// Negative control: an unknown key must still be rejected, so the strict
	// contract is not quietly relaxed by the new field. (Case is NOT part of
	// that contract — encoding/json matches field names case-insensitively, so
	// `maxSLOs` is accepted as the same key.)
	var typo EntitlementLimits
	r.Error(json.Unmarshal([]byte(`{"maxSloCount":3}`), &typo))

	var casing EntitlementLimits
	r.NoError(json.Unmarshal([]byte(`{"maxSLOs":3}`), &casing))
	r.NotNil(casing.MaxSlos)
}
