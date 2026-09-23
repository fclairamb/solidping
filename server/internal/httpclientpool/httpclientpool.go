// Package httpclientpool builds *http.Client instances for outbound
// integration and notification calls, all backed by one process-wide
// connection pool that is deliberately NOT http.DefaultTransport.
package httpclientpool

import (
	"net/http"
	"sync"
	"time"
)

// transport is the shared connection pool every client built by NewClient
// uses, deliberately NOT http.DefaultTransport.
//
// httptest.Server.Close() reaches into the global http.DefaultTransport and
// calls CloseIdleConnections() on it — see net/http/httptest/server.go, "assume
// most users of httptest.Server will be using the standard transport, so help
// them out". A client built as a bare &http.Client{Timeout: ...} inherits that
// global, so in a package whose tests run in parallel against several httptest
// servers, one test's deferred srv.Close() can tear connections out from under
// another test's in-flight request. That surfaces as
//
//	net/http: HTTP/1.x transport connection broken: http: CloseIdleConnections called
//
// which reads like a network flake but is a real shared-state race, and it
// only fires under enough parallelism to be CI-only. Owning our own transport
// makes callers immune to it, and is better production hygiene regardless:
// outbound integration traffic no longer shares a connection pool (or anyone
// else's CloseIdleConnections calls) with every other HTTP client in the
// process.
//
//nolint:gochecknoglobals // one process-wide connection pool, intentionally shared.
var (
	transportOnce sync.Once
	transport     http.RoundTripper
)

// NewClient returns an *http.Client with the given timeout, backed by one
// process-wide transport cloned from http.DefaultTransport (not the global
// itself — see the package doc comment on transport).
func NewClient(timeout time.Duration) *http.Client {
	transportOnce.Do(func() {
		// Clone the standard transport so we keep its proxy, dial and TLS
		// defaults, then own the resulting pool ourselves.
		base, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			transport = http.DefaultTransport

			return
		}

		transport = base.Clone()
	})

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}
