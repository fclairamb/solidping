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
