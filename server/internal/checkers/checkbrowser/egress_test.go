package checkbrowser

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/egress"
)

// Chrome cannot take the guard's dialer, so a navigation is pre-flighted: a
// URL whose host resolves to a non-public address is refused before Chrome is
// asked to go there. Anything else (a permissive policy, a name that does not
// resolve here) is left to Chrome exactly as before.
func TestPreflightEgress(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	deny := egress.WithGuard(t.Context(), egress.New(false))
	r.ErrorIs(preflightEgress(deny, "http://127.0.0.1:4000/api/v1/"), egress.ErrDenied)
	r.ErrorIs(preflightEgress(deny, "http://169.254.169.254/latest/meta-data/"), egress.ErrDenied)
	r.ErrorIs(preflightEgress(deny, "https://[::1]/"), egress.ErrDenied)

	brokenDNS := egress.WithGuard(t.Context(), egress.New(false, egress.WithLookup(
		func(context.Context, string) ([]net.IPAddr, error) {
			return nil, &net.DNSError{Err: "no such host", Name: "unresolvable.invalid", IsNotFound: true}
		},
	)))
	r.NoError(preflightEgress(brokenDNS, "https://unresolvable.invalid/"), "Chrome reports its own resolve errors")
	r.NoError(preflightEgress(deny, "about:blank"))
	r.NoError(preflightEgress(deny, "https://8.8.8.8/"))

	allow := egress.WithGuard(t.Context(), egress.New(true))
	r.NoError(preflightEgress(allow, "http://127.0.0.1:4000/"))
	r.NoError(preflightEgress(t.Context(), "http://127.0.0.1:4000/"))
}
