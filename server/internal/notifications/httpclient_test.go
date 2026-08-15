package notifications

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
// http.DefaultTransport, so as long as the senders ride that global, one
// parallel test's teardown can break another's in-flight send. Asserting that
// we hold our own transport is what makes that unreachable — a version of
// newHTTPClient that merely *intends* to clone (and assigns
// http.DefaultTransport in both branches) still passes the sender tests
// locally, but fails here.
func TestNewHTTPClientDoesNotUseTheGlobalTransport(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	client := newHTTPClient(time.Second)
	r.NotNil(client.Transport)
	r.NotSame(http.DefaultTransport, client.Transport, "senders must not ride http.DefaultTransport")

	// It must still be a real *http.Transport carrying the standard proxy /
	// dial / TLS defaults, not some bare stand-in.
	transport, ok := client.Transport.(*http.Transport)
	r.True(ok)

	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	r.True(ok)
	r.Equal(defaultTransport.TLSHandshakeTimeout, transport.TLSHandshakeTimeout)
	r.Equal(defaultTransport.MaxIdleConns, transport.MaxIdleConns)
}

// One pool for the whole package: every sender shares the connection pool
// rather than building a fresh one per send.
func TestNewHTTPClientSharesOnePool(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	first := newHTTPClient(time.Second)
	second := newHTTPClient(2 * time.Second)

	r.Same(first.Transport, second.Transport)
	r.NotSame(first, second)
	r.Equal(time.Second, first.Timeout)
	r.Equal(2*time.Second, second.Timeout)
}
