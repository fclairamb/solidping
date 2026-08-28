package uistate

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The key allowlist is the whole reason this store cannot become a junk
// drawer, so it gets pinned directly. `isOrgRefShape` is the half that runs
// before any database lookup — an unresolvable-but-well-shaped reference is
// covered end to end in test/integration/uistate_test.go.
func TestIsOrgRefShape(t *testing.T) {
	t.Parallel()

	t.Run("accepts a slug and a uid", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		r.True(isOrgRefShape("acme"))
		r.True(isOrgRefShape("acme-tech"))
		r.True(isOrgRefShape("Acme2"))
		r.True(isOrgRefShape("2f1c9f0e-1f2a-4a3b-8c4d-5e6f7a8b9c0d"))
	})

	t.Run("rejects anything that is not an org reference", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		// Positive control: the same characters minus the offending one are
		// accepted, so each rejection below is about the character and not
		// about the shape of the test.
		r.True(isOrgRefShape("acme"))

		r.False(isOrgRefShape(""), "empty")
		r.False(isOrgRefShape("acme/evil"), "path separator")
		r.False(isOrgRefShape("acme.evil"), "a dot would let a second key segment through")
		r.False(isOrgRefShape("acme evil"), "space")
		r.False(isOrgRefShape("acme_evil"), "underscore")
		r.False(isOrgRefShape("acme%00"), "percent")
		r.False(isOrgRefShape(string(make([]byte, maxOrgRefLength+1))), "over the length cap")
	})
}

// ResolveKey's prefix check runs before any database call, so a bad prefix is
// testable without a service.
func TestResolveKeyRejectsForeignPrefixes(t *testing.T) {
	t.Parallel()

	svc := NewService(nil)

	for _, key := range []string{
		"",
		"onboarding",
		"onboarding.",
		"theme.acme",
		"onboarding.acme.extra",
		"../onboarding.acme",
		"ONBOARDING.acme",
	} {
		_, err := svc.ResolveKey(t.Context(), key)
		require.ErrorIs(t, err, ErrInvalidKey, "key %q must be rejected", key)
	}
}

// The size cap is a constant the handler enforces; pin it so a future edit
// that loosens it is a deliberate, visible change rather than a typo.
func TestMaxValueBytesStaysSmall(t *testing.T) {
	t.Parallel()

	require.Equal(t, 4096, MaxValueBytes)
}
