package reportschedules

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Recipients are PII and drive real outbound mail, so the normalizer is the one
// place that decides what an operator's paste actually becomes.
func TestNormalizeRecipients(t *testing.T) {
	t.Parallel()

	t.Run("trims, drops blanks and de-duplicates case-insensitively", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		got, err := normalizeRecipients([]string{
			" alice@acme.com ", "", "ALICE@acme.com", "bob@acme.com", "   ",
		})
		r.NoError(err)
		// The FIRST spelling wins, so the operator sees back what they typed.
		r.Equal([]string{"alice@acme.com", "bob@acme.com"}, got)
	})

	t.Run("an invalid address is rejected outright", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		// Positive control: the same list without the bad entry is accepted, so
		// the rejection below is about the address and not about the shape.
		good, err := normalizeRecipients([]string{"alice@acme.com"})
		r.NoError(err)
		r.Len(good, 1)

		_, err = normalizeRecipients([]string{"alice@acme.com", "not-an-address"})
		r.ErrorIs(err, ErrInvalidRecipient)
	})

	t.Run("an oversized list is rejected", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		many := make([]string, maxRecipients+1)
		for i := range many {
			many[i] = "a@acme.com"
		}

		_, err := normalizeRecipients(many)
		r.ErrorIs(err, ErrTooManyRecipients)
	})

	t.Run("nil becomes an empty list, never nil", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		got, err := normalizeRecipients(nil)
		r.NoError(err)
		r.NotNil(got)
		r.Empty(got)
	})
}

func TestNormalizeUIDs(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	got := normalizeUIDs([]string{" a ", "b", "a", "", "b"})
	r.Equal([]string{"a", "b"}, got)
	r.NotNil(normalizeUIDs(nil))
}
