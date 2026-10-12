package checkhttp

import (
	"crypto/tls"
	"net/http/httptrace"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Metric keys written on an HTTP result (spec 2026-10-12-08). A phase that did
// not happen writes no key, never 0 (same rule as gRPC).
const (
	metricDNSTime      = "dns_time_ms"
	metricConnectTime  = "connect_time_ms"
	metricTLSTime      = "tls_time_ms"
	metricTTFBTime     = "ttfb_time_ms"
	metricDownloadTime = "download_time_ms"
	metricRedirectTime = "redirect_time_ms"

	microsecondsPerMilli = 1000.0
)

// durationMs converts a duration to fractional milliseconds.
func durationMs(d time.Duration) float64 {
	return float64(d.Microseconds()) / microsecondsPerMilli
}

// phaseTimer records the per-phase timings of a probe from httptrace hooks.
// The dial hooks fire on other goroutines, hence the mutex. Per-hop state is
// reset at every GetConn, so the phases describe the final hop of a redirect
// chain; the earlier hops are summarised by redirect_time_ms.
type phaseTimer struct {
	mu    sync.Mutex
	start time.Time

	hops      int
	finalHop  time.Time // GetConn of the latest hop
	dnsStart  time.Time
	dnsDone   time.Time
	connStart time.Time
	connDone  time.Time
	tlsStart  time.Time
	tlsDone   time.Time
	wrote     time.Time
	firstByte time.Time
}

func newPhaseTimer(start time.Time) *phaseTimer {
	return &phaseTimer{start: start}
}

func (p *phaseTimer) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn: func(string) {
			p.mu.Lock()
			defer p.mu.Unlock()

			p.hops++
			p.finalHop = time.Now()
			p.dnsStart, p.dnsDone = time.Time{}, time.Time{}
			p.connStart, p.connDone = time.Time{}, time.Time{}
			p.tlsStart, p.tlsDone = time.Time{}, time.Time{}
			p.wrote, p.firstByte = time.Time{}, time.Time{}
		},
		DNSStart: func(httptrace.DNSStartInfo) {
			p.mu.Lock()
			defer p.mu.Unlock()

			if p.dnsStart.IsZero() {
				p.dnsStart = time.Now()
			}
		},
		DNSDone: func(httptrace.DNSDoneInfo) {
			p.mu.Lock()
			defer p.mu.Unlock()

			if p.dnsDone.IsZero() {
				p.dnsDone = time.Now()
			}
		},
		ConnectStart: func(_, _ string) {
			p.mu.Lock()
			defer p.mu.Unlock()

			if p.connStart.IsZero() {
				p.connStart = time.Now()
			}
		},
		ConnectDone: func(_, _ string, err error) {
			p.mu.Lock()
			defer p.mu.Unlock()

			if err == nil && p.connDone.IsZero() {
				p.connDone = time.Now()
			}
		},
		TLSHandshakeStart: func() {
			p.mu.Lock()
			defer p.mu.Unlock()

			p.tlsStart = time.Now()
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			p.mu.Lock()
			defer p.mu.Unlock()

			if err == nil {
				p.tlsDone = time.Now()
			}
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			p.mu.Lock()
			defer p.mu.Unlock()

			p.wrote = time.Now()
		},
		GotFirstResponseByte: func() {
			p.mu.Lock()
			defer p.mu.Unlock()

			p.firstByte = time.Now()
		},
	}
}

// metrics returns the recorded phases. download is the body read time, zero
// when the probe did not read the body.
func (p *phaseTimer) metrics(download time.Duration, downloaded bool) map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make(map[string]any)

	// A lookup still running when the probe ended (timeout) is reported up to now.
	if !p.dnsStart.IsZero() {
		end := p.dnsDone
		if end.IsZero() {
			end = time.Now()
		}

		out[metricDNSTime] = durationMs(end.Sub(p.dnsStart))
	}

	if !p.connStart.IsZero() && !p.connDone.IsZero() {
		out[metricConnectTime] = durationMs(p.connDone.Sub(p.connStart))
	}

	if !p.tlsStart.IsZero() && !p.tlsDone.IsZero() {
		out[metricTLSTime] = durationMs(p.tlsDone.Sub(p.tlsStart))
	}

	if !p.wrote.IsZero() && !p.firstByte.IsZero() {
		out[metricTTFBTime] = durationMs(p.firstByte.Sub(p.wrote))
	}

	if downloaded {
		out[metricDownloadTime] = durationMs(download)
	}

	if p.hops > 1 {
		out[metricRedirectTime] = durationMs(p.finalHop.Sub(p.start))
	}

	return out
}

// stampTimings merges the phase metrics onto a result, keeping any metric the
// result already carries.
func stampTimings(result *checkerdef.Result, timer *phaseTimer, info *responseInfo) {
	m := timer.metrics(info.downloadTime, info.downloaded)
	if len(m) == 0 {
		return
	}

	if result.Metrics == nil {
		result.Metrics = make(map[string]any, len(m))
	}

	for k, v := range m {
		if _, exists := result.Metrics[k]; !exists {
			result.Metrics[k] = v
		}
	}
}
