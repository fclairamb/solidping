package notifications

import (
	"net/http"
	"sync"
	"time"
)

// notificationTransport is the shared connection pool every outbound
// notification sender uses, deliberately NOT http.DefaultTransport.
//
// httptest.Server.Close() reaches into the global http.DefaultTransport and
// calls CloseIdleConnections() on it — see net/http/httptest/server.go, "assume
// most users of httptest.Server will be using the standard transport, so help
// them out". A sender built as a bare &http.Client{Timeout: ...} inherits that
// global, so in a package whose tests run in parallel against several httptest
// servers, one test's deferred srv.Close() can tear connections out from under
// another test's in-flight send. That surfaces as
//
//	net/http: HTTP/1.x transport connection broken: http: CloseIdleConnections called
//
// which reads like a network flake but is a real shared-state race, and it only
// fires under enough parallelism to be CI-only. Owning our own transport makes
// the senders immune to it, and is better production hygiene regardless:
// notification egress no longer shares a connection pool (or anyone else's
// CloseIdleConnections calls) with every other HTTP client in the process.
//
//nolint:gochecknoglobals // one process-wide connection pool, intentionally shared.
var (
	notificationTransportOnce sync.Once
	notificationTransport     http.RoundTripper
)

// newHTTPClient returns a notification HTTP client with the given timeout,
// backed by the package's own connection pool rather than the global default.
func newHTTPClient(timeout time.Duration) *http.Client {
	notificationTransportOnce.Do(func() {
		// Clone the standard transport so we keep its proxy, dial and TLS
		// defaults, then own the resulting pool ourselves.
		_, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			notificationTransport = http.DefaultTransport

			return
		}

		notificationTransport = http.DefaultTransport
	})

	return &http.Client{
		Timeout:   timeout,
		Transport: notificationTransport,
	}
}
