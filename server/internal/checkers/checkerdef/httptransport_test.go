package checkerdef

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/egress"
)

func TestHTTPProbeTransportFor_HTTP11KeepsTheSharedTransport(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for _, version := range []HTTPVersion{"", HTTPVersion11} {
		transport, release := HTTPProbeTransportFor(context.Background(), false, version)
		r.Same(checkTransport, transport, "HTTP/1.1 must keep the shared pooled transport")
		release()
	}

	guard := egress.New(false)
	ctx := egress.WithGuard(context.Background(), guard)

	transport, release := HTTPProbeTransportFor(ctx, false, HTTPVersion11)
	defer release()

	r.Same(guard.HTTPTransport(), transport, "HTTP/1.1 under an enforcing guard keeps the guard's pooled transport")
}

func TestHTTPProbeTransportFor_HTTP2IsFreshAndUnpooled(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	first, releaseFirst := HTTPProbeTransportFor(context.Background(), false, HTTPVersion2)
	defer releaseFirst()

	second, releaseSecond := HTTPProbeTransportFor(context.Background(), false, HTTPVersion2)
	defer releaseSecond()

	r.NotSame(first, second, "each HTTP/2 probe must get its own transport")
	r.NotSame(checkTransport, first)

	transport, ok := first.(*http.Transport)
	r.True(ok)
	r.True(transport.DisableKeepAlives, "an HTTP/2 probe transport must not keep connections alive")
	r.NotNil(transport.Protocols)
	r.True(transport.Protocols.HTTP2())
	r.True(transport.Protocols.UnencryptedHTTP2())
	r.False(transport.Protocols.HTTP1(), "HTTP/2 is forced, never negotiated down to HTTP/1.1")
}

func TestHTTPProbeTransportFor_HTTP3IsFresh(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	first, releaseFirst := HTTPProbeTransportFor(context.Background(), false, HTTPVersion3)
	defer releaseFirst()

	second, releaseSecond := HTTPProbeTransportFor(context.Background(), false, HTTPVersion3)
	defer releaseSecond()

	r.NotSame(first, second)
}

func TestHTTPProbeTransportFor_HTTP3RefusesATunnel(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	transport, release := buildProbeTransport(&net.Dialer{}, false, IPVersionAuto, nil, HTTPVersion3)
	defer release()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://acme.com/", nil)
	r.NoError(err)

	resp, err := transport.RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}

	r.ErrorIs(err, ErrHTTP3OverTunnel)
}

// TestHTTPProbeTransportFor_EgressGuardRefusesPrivate pins that forcing HTTP/2
// or HTTP/3 does not open a way around the egress guard: a private target is
// refused before anything is dialed, by IP literal and by name.
func TestHTTPProbeTransportFor_EgressGuardRefusesPrivate(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	require.NoError(t, err)

	guard := egress.New(false, egress.WithLookup(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}))

	for _, version := range []HTTPVersion{HTTPVersion2, HTTPVersion3} {
		for _, ipVersion := range []IPVersion{IPVersionAuto, IPVersionIPv4} {
			for _, host := range []string{"127.0.0.1", "internal.acme.com"} {
				if ipVersion == IPVersionIPv4 && host != "127.0.0.1" {
					// The pinned path resolves through checkerdef's resolver,
					// not the guard's test lookup.
					continue
				}

				t.Run(string(version)+"/"+ipVersion.String()+"/"+host, func(t *testing.T) {
					t.Parallel()

					r := require.New(t)

					ctx := egress.WithGuard(context.Background(), guard)
					ctx = WithIPVersion(ctx, ipVersion)
					ctx, cancel := context.WithTimeout(ctx, 3*time.Second)

					defer cancel()

					transport, release := HTTPProbeTransportFor(ctx, true, version)
					defer release()

					scheme := "https"
					if version == HTTPVersion2 {
						scheme = "http"
					}

					req, reqErr := http.NewRequestWithContext(
						ctx, http.MethodGet, scheme+"://"+net.JoinHostPort(host, port)+"/", nil)
					r.NoError(reqErr)

					resp, rtErr := transport.RoundTrip(req)
					if resp != nil {
						_ = resp.Body.Close()
					}

					r.Error(rtErr)
					r.ErrorIs(rtErr, egress.ErrDenied)
				})
			}
		}
	}
}

// TestStrandedConnectionDoesNotSurviveTheProbeThatHitIt_HTTP2 reruns the
// 2026-09-28-04 regression with `httpVersion: "2"`: the probe after a stranded
// HTTP/2 one must answer, on a connection of its own.
func TestStrandedConnectionDoesNotSurviveTheProbeThatHitIt_HTTP2(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server, seen := strandedFirstConnServer(t)
	defer server.Close()

	// probe returns the status code and protocol major of one probe.
	probe := func() (int, int, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		defer cancel()

		// One transport per probe, released right after, exactly as the HTTP
		// checker does.
		transport, release := HTTPProbeTransportFor(ctx, true, HTTPVersion2)
		defer release()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
		if err != nil {
			return 0, 0, err
		}

		resp, err := (&http.Client{Transport: transport}).Do(req)
		if err != nil {
			return 0, 0, err
		}

		_ = resp.Body.Close()

		return resp.StatusCode, resp.ProtoMajor, nil
	}

	_, _, err := probe()
	r.Error(err, "the first probe must time out on the stranded connection")

	status, protoMajor, err := probe()
	r.NoError(err, "the probe after a stranded one must dial fresh, not inherit the corpse")
	r.Equal(http.StatusOK, status)
	r.Equal(2, protoMajor)

	conns := seen()
	r.Len(conns, 2)
	r.NotEqual(conns[0], conns[1])
}
