package twilio

import (
	"net/http"
	"sync"
	"time"
)

// twilioTransport is the connection pool every Twilio call uses, deliberately
// NOT http.DefaultTransport: httptest.Server.Close() calls CloseIdleConnections()
// on the global default, which breaks an in-flight request of a parallel test
// ("http: CloseIdleConnections called"). Same fix as notifications/httpclient.go.
//
//nolint:gochecknoglobals // one process-wide connection pool, intentionally shared.
var (
	twilioTransportOnce sync.Once
	twilioTransport     http.RoundTripper
)

func newHTTPClient(timeout time.Duration) *http.Client {
	twilioTransportOnce.Do(func() {
		base, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			twilioTransport = http.DefaultTransport

			return
		}

		twilioTransport = base.Clone()
	})

	return &http.Client{
		Timeout:   timeout,
		Transport: twilioTransport,
	}
}
