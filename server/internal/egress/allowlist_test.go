package egress_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/egress"
)

func TestHostAllowlistCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	list := egress.NewHostAllowlist([]string{"Acme.com.", "127.0.0.1", "[::1]"})

	r.NoError(list.Check("acme.com"))
	r.NoError(list.Check("ACME.COM:443"))
	r.NoError(list.Check("127.0.0.1:8080"))
	r.NoError(list.Check("[::1]:80"))
	r.Nil(list.Refused())

	err := list.Check("api.acme.com")
	r.ErrorIs(err, egress.ErrHostNotAllowed)
	r.Contains(err.Error(), "host not allowed while secrets are loaded: api.acme.com")
	r.Error(list.Check(""))
	r.Equal("api.acme.com", list.Refused().Target, "the first refusal is kept")

	r.ErrorIs(egress.NewHostAllowlist(nil).Check("acme.com"), egress.ErrHostNotAllowed, "an empty list allows nothing")

	var none *egress.HostAllowlist
	r.NoError(none.Check("anything.example"))
	r.NoError(none.Refuse("browser"))
	r.Nil(none.Refused())
	r.Nil(egress.HostAllowlistFrom(context.Background()))
	r.Same(list, egress.HostAllowlistFrom(egress.WithHostAllowlist(context.Background(), list)))
}

// The round tripper checks every hop, so a redirect to another host is
// refused before it is sent.
func TestHostAllowlistRoundTripperRefusesARedirect(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var hits atomic.Int32

	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(other.Close)

	// 127.0.0.1 redirects to localhost: same machine, another host name.
	start := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, port, _ := net.SplitHostPort(other.Listener.Addr().String())
		http.Redirect(w, req, "http://localhost:"+port+"/", http.StatusFound)
	}))
	t.Cleanup(start.Close)

	list := egress.NewHostAllowlist([]string{"127.0.0.1"})
	client := &http.Client{Transport: list.RoundTripper(http.DefaultTransport)}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, start.URL, nil)
	r.NoError(err)

	resp, err := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}

	r.Error(err)
	r.ErrorIs(err, egress.ErrHostNotAllowed)
	r.Zero(hits.Load(), "the redirect target never saw the request")

	// Positive control: allowed, the redirect is followed.
	open := &http.Client{Transport: egress.NewHostAllowlist([]string{"127.0.0.1", "localhost"}).RoundTripper(nil)}
	req, err = http.NewRequestWithContext(t.Context(), http.MethodGet, start.URL, nil)
	r.NoError(err)
	resp, err = open.Do(req)
	r.NoError(err)
	_ = resp.Body.Close()
	r.Equal(int32(1), hits.Load())
}
