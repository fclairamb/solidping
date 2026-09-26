package httpclientpool

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The regression guard for the CI-only failure
//
//	sending webhook: Post "http://127.0.0.1:36863": net/http: HTTP/1.x
//	transport connection broken: http: CloseIdleConnections called
//
// httptest.Server.Close() calls CloseIdleConnections() on the *global*
// http.DefaultTransport, so as long as callers ride that global, one parallel
// test's teardown can break another's in-flight request. Asserting that we
// hold our own transport is what makes that unreachable — a version of
// NewClient that merely *intends* to clone (and assigns http.DefaultTransport
// in both branches) still passes callers' own tests locally, but fails here.
func TestNewClientKeepsTheRequestedTimeout(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	client := NewClient(3 * time.Second)
	r.Equal(3*time.Second, client.Timeout)
}

func TestNewClientDoesNotUseTheGlobalTransport(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	client := NewClient(time.Second)
	r.NotNil(client.Transport)
	r.NotSame(http.DefaultTransport, client.Transport, "callers must not ride http.DefaultTransport")

	// It must still be a real *http.Transport carrying the standard proxy /
	// dial / TLS defaults, not some bare stand-in.
	tr, ok := client.Transport.(*http.Transport)
	r.True(ok)

	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	r.True(ok)
	r.Equal(defaultTransport.TLSHandshakeTimeout, tr.TLSHandshakeTimeout)
	r.Equal(defaultTransport.MaxIdleConns, tr.MaxIdleConns)
}

// One pool for the whole process: every client shares the connection pool
// rather than building a fresh one per call.
func TestNewClientSharesOnePool(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	first := NewClient(time.Second)
	second := NewClient(2 * time.Second)

	r.Same(first.Transport, second.Transport)
	r.NotSame(first, second)
	r.Equal(time.Second, first.Timeout)
	r.Equal(2*time.Second, second.Timeout)
}
