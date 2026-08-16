package models_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestCapabilityIsTriState is the smallest possible statement of the invariant
// the whole spec protects: a nil set is UNKNOWN, and a non-nil set is closed —
// absence from it is a real "no". Two states here would be the regression.
func TestCapabilityIsTriState(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal(models.CapabilityStateUnknown,
		(&models.Worker{}).Capability(models.CapabilityIPv6),
		"a worker that never reported must be UNKNOWN, never absent")
	r.Equal(models.CapabilityStateAbsent,
		(&models.Worker{Capabilities: []string{}}).Capability(models.CapabilityIPv6),
		"an empty REPORTED set is a real no")
	r.Equal(models.CapabilityStateAbsent,
		(&models.Worker{Capabilities: []string{"ipv4"}}).Capability(models.CapabilityIPv6))
	r.Equal(models.CapabilityStatePresent,
		(&models.Worker{Capabilities: []string{"ipv4", "ipv6"}}).Capability(models.CapabilityIPv6))

	// Unknown and absent must never be the same value.
	r.NotEqual(models.CapabilityStateUnknown, models.CapabilityStateAbsent)
}

// TestValidateCapabilitySetMirrorsTheDatabase: the Go guard rejects exactly the
// element shapes the database CHECK constraint and SQLite triggers reject, so a
// set that passes here is storable on both dialects.
func TestValidateCapabilitySetMirrorsTheDatabase(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		caps  []string
		valid bool
	}{
		{"nil-is-not-reported", nil, true},
		{"empty-set", []string{}, true},
		{"slugs", []string{"ipv4", "ipv6", "a-b", "c9", "1"}, true},
		{"prefix-is-not-a-duplicate", []string{"a", "ab", "b"}, true},
		{"empty-name", []string{""}, false},
		{"empty-among-others", []string{"ipv4", ""}, false},
		{"duplicate", []string{"ipv4", "ipv4"}, false},
		{"duplicate-separated", []string{"ipv4", "ipv6", "ipv4"}, false},
		{"uppercase", []string{"IPv6"}, false},
		{"underscore", []string{"ip_v6"}, false},
		{"dot", []string{"tls.13"}, false},
		{"space", []string{" "}, false},
		{"embedded-space", []string{"a b"}, false},
		{"comma", []string{"a,b"}, false},
		{"non-ascii", []string{"ipv6é"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := models.ValidateCapabilitySet(tc.caps)
			if tc.valid {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, models.ErrInvalidCapabilitySet)
		})
	}
}
