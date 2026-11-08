package email

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidRecipients(t *testing.T) {
	t.Parallel()

	valid, invalid := ValidRecipients([]string{"a@acme.com", "not-an-address", "Bob <b@acme.com>", ""})

	require.Equal(t, []string{"a@acme.com", "Bob <b@acme.com>"}, valid)
	require.Equal(t, []string{"not-an-address", ""}, invalid)
}

func TestRedactAddress(t *testing.T) {
	t.Parallel()

	require.Equal(t, "***@acme.com", RedactAddress("alice@acme.com"))
	require.Equal(t, "***", RedactAddress("not-an-address"))
	require.Equal(t, []string{"***@acme.com", "***"}, RedactAddresses([]string{"x@acme.com", "junk"}))
}
