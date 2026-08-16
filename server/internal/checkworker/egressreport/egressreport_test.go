package egressreport_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/egressreport"
)

// TestFromKeepsUnknownDistinctFromNone is the producer-side half of the
// three-state guarantee. A probe that answered for nothing must yield a NIL
// slice ("unknown"), while a probe that answered and found nothing must yield a
// NON-NIL empty slice ("none"). Returning an empty slice for the first case
// would tell the server "this host has no IPv6" on the strength of no evidence
// at all — the exact false statement the feature exists to remove.
func TestFromKeepsUnknownDistinctFromNone(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	unknown := egressreport.From(map[checkerdef.IPVersion]bool{})
	r.Nil(unknown, "a probe that answered for no family at all is UNKNOWN, not none")

	none := egressreport.From(map[checkerdef.IPVersion]bool{
		checkerdef.IPVersionIPv4: false,
		checkerdef.IPVersionIPv6: false,
	})
	r.NotNil(none, "a probe that answered and found nothing is a real report of NONE")
	r.Empty(none)
}

// TestFromRendersTheSubsetThatIsTrue: only families the probe found are named,
// and the result is a closed set — a family that came back false is simply
// absent, which downstream means "no" precisely because the slice is non-nil.
func TestFromRendersTheSubsetThatIsTrue(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		families map[checkerdef.IPVersion]bool
		want     []string
	}{
		{
			"dual-stack",
			map[checkerdef.IPVersion]bool{
				checkerdef.IPVersionIPv4: true, checkerdef.IPVersionIPv6: true,
			},
			[]string{"ipv4", "ipv6"},
		},
		{
			"v4-only",
			map[checkerdef.IPVersion]bool{
				checkerdef.IPVersionIPv4: true, checkerdef.IPVersionIPv6: false,
			},
			[]string{"ipv4"},
		},
		{
			"v6-only",
			map[checkerdef.IPVersion]bool{
				checkerdef.IPVersionIPv4: false, checkerdef.IPVersionIPv6: true,
			},
			[]string{"ipv6"},
		},
		{
			"partial-probe-answering-only-v6",
			map[checkerdef.IPVersion]bool{checkerdef.IPVersionIPv6: true},
			[]string{"ipv6"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, egressreport.From(tc.families))
		})
	}
}

// TestFromEmitsOnlyStorableNames: every name the producer can emit must satisfy
// the database CHECK constraint — lowercase slugs, no duplicates. A producer
// that emitted anything else would fail the write at runtime rather than here.
func TestFromEmitsOnlyStorableNames(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	names := egressreport.From(map[checkerdef.IPVersion]bool{
		checkerdef.IPVersionIPv4: true, checkerdef.IPVersionIPv6: true,
	})

	seen := map[string]bool{}

	for _, name := range names {
		r.NotEmpty(name)
		r.False(seen[name], "the capability set must not contain duplicates")
		seen[name] = true

		for _, char := range name {
			r.True((char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-',
				"capability %q must stay inside the [a-z0-9-] slug charset", name)
		}
	}
}
