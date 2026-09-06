package checkhttp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	sampleExpectedStatus = 200 // HTTP 200 OK for sample checks
	sampleRedirectStatus = 301 // HTTP 301 Moved Permanently, for the no-follow-redirects sample
	defaultBaseURL       = "http://localhost:4000"
	methodGET            = "GET"
	statusCodePattern2XX = "2XX" // matches any 2xx response code
)

func baseURL(opts *checkerdef.ListSampleOptions) string {
	if opts != nil && opts.BaseURL != "" {
		return opts.BaseURL
	}

	return defaultBaseURL
}

// GetSampleConfigs returns sample HTTP check configurations.
//
//nolint:funlen // enumerating many sample configs is inherently verbose
func (c *HTTPChecker) GetSampleConfigs(opts *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	base := baseURL(opts)

	// Test samples - use local fake API endpoints with diverse failure patterns
	if opts != nil && opts.Type == checkerdef.Test {
		return []checkerdef.CheckSpec{
			{
				Name:   "Fake API (Stable)",
				Slug:   "http-fake-stable",
				Period: time.Second * 10,
				Config: (&HTTPConfig{
					URL:            base + "/api/v1/fake?period=86400",
					Method:         methodGET,
					ExpectedStatus: sampleExpectedStatus,
				}).GetConfig(),
			},
			{
				Name:   "Fake API (Flaky)",
				Slug:   "http-fake-flaky",
				Period: time.Second * 15,
				Config: (&HTTPConfig{
					URL:            base + "/api/v1/fake?period=120",
					Method:         methodGET,
					ExpectedStatus: sampleExpectedStatus,
				}).GetConfig(),
			},
			{
				Name:   "Fake API (Unstable)",
				Slug:   "http-fake-unstable",
				Period: time.Second * 15,
				Config: (&HTTPConfig{
					URL:            base + "/api/v1/fake?period=40",
					Method:         methodGET,
					ExpectedStatus: sampleExpectedStatus,
				}).GetConfig(),
			},
			{
				Name:   "Fake API (Slow)",
				Slug:   "http-fake-slow",
				Period: time.Second * 20,
				Config: (&HTTPConfig{
					URL:            base + "/api/v1/fake?period=86400&delay=2000",
					Method:         methodGET,
					ExpectedStatus: sampleExpectedStatus,
				}).GetConfig(),
			},
			{
				Name:   "Fake API (503 errors)",
				Slug:   "http-fake-503",
				Period: time.Second * 15,
				Config: (&HTTPConfig{
					URL:            base + "/api/v1/fake?period=60&statusDown=503",
					Method:         methodGET,
					ExpectedStatus: sampleExpectedStatus,
				}).GetConfig(),
			},
		}
	}

	// Demo samples — the public live demo's catalogue (spec 2026-09-06-02).
	//
	// TARGETS ARE SOLIDPING-OWNED ONLY. The demo runs from every public region
	// we have, forever, on a page anyone can open; pointing that at somebody
	// else's server would be spending a third party's bandwidth to advertise
	// ours. So: our marketing site, our docs, our status page, our own health
	// endpoint, and the /fake fixture for the interesting failure shapes.
	//
	// SIZING AGAINST THE /fake LIMITER. `/api/v1/fake` is rate-limited to 60
	// requests per minute PER CLIENT IP (fakeAPIRequestsPerMinute in
	// internal/app/server.go), and each worker region is a distinct client IP.
	// Three fake-backed checks at 60s = 3 requests/minute/region, comfortably
	// under 60 even once visitors add their own — and a visitor's own checks
	// are floored at 60s too. Adding many more fake-backed checks, or
	// shortening any of these below a minute, is what would start eating that
	// headroom.
	if opts != nil && opts.Type == checkerdef.Demo {
		return []checkerdef.CheckSpec{
			{
				// The steady green line: our own API health endpoint, probed
				// from every public region.
				Name:   "SolidPing API",
				Slug:   "demo-api-health",
				Period: time.Minute,
				Config: (&HTTPConfig{
					URL:                 base + "/api/mgmt/health",
					Method:              methodGET,
					ExpectedStatusCodes: []string{statusCodePattern2XX},
				}).GetConfig(),
			},
			{
				Name:   "SolidPing docs",
				Slug:   "demo-docs",
				Period: time.Minute,
				Config: (&HTTPConfig{
					URL:                 base + "/docs/",
					Method:              methodGET,
					ExpectedStatusCodes: []string{statusCodePattern2XX},
				}).GetConfig(),
			},
			{
				// The flapping one — something is always moving on the
				// dashboard, which is the point of a demo.
				Name:   "Flaky service",
				Slug:   "demo-flaky-service",
				Period: time.Minute,
				Config: (&HTTPConfig{
					URL:            base + "/api/v1/fake?period=600",
					Method:         methodGET,
					ExpectedStatus: sampleExpectedStatus,
				}).GetConfig(),
			},
			{
				// The slow one: up, but visibly worse than its neighbours, so
				// the response-time chart has something to say.
				Name:   "Slow endpoint",
				Slug:   "demo-slow-endpoint",
				Period: time.Minute,
				Config: (&HTTPConfig{
					// statusUp == statusDown == 200 pins it UP: /fake's state
					// flips every `period` seconds, and a demo's "slow but
					// healthy" example must not quietly become an outage
					// halfway through the day.
					URL:            base + "/api/v1/fake?period=86400&statusUp=200&statusDown=200&delay=1500",
					Method:         methodGET,
					ExpectedStatus: sampleExpectedStatus,
				}).GetConfig(),
			},
			{
				// The hard-down one: a permanent 503, so there is always a
				// real open incident and a real escalation to look at.
				Name:   "Legacy billing API",
				Slug:   "demo-legacy-billing",
				Period: time.Minute,
				Config: (&HTTPConfig{
					// Both states 503, so it is down ALWAYS rather than half
					// the time — the demo needs one permanently open incident
					// with a real escalation trail behind it.
					URL:            base + "/api/v1/fake?period=86400&statusUp=503&statusDown=503",
					Method:         methodGET,
					ExpectedStatus: sampleExpectedStatus,
				}).GetConfig(),
			},
			{
				Name:   "Status page",
				Slug:   "demo-status-page",
				Period: time.Minute,
				Config: (&HTTPConfig{
					URL:                 base + "/status0/",
					Method:              methodGET,
					ExpectedStatusCodes: []string{statusCodePattern2XX},
				}).GetConfig(),
			},
		}
	}

	// Default samples
	return []checkerdef.CheckSpec{
		{
			Name:   "Test API",
			Slug:   "http-test-api",
			Period: time.Second * 20,
			Config: (&HTTPConfig{
				URL:            base + "/api/v1/fake?period=70",
				Method:         methodGET,
				ExpectedStatus: sampleExpectedStatus,
			}).GetConfig(),
		},
		{
			Name:   "Cloudflare DNS",
			Slug:   "http-cloudflare-dns",
			Period: time.Minute,
			Config: (&HTTPConfig{
				URL:            "https://one.one.one.one",
				Method:         methodGET,
				ExpectedStatus: sampleExpectedStatus,
			}).GetConfig(),
		},
		{
			Name:   "Claude API",
			Slug:   "http-claude-api",
			Period: time.Minute,
			Config: (&HTTPConfig{
				URL:                 "https://status.claude.com/api/v2/status.json",
				Method:              methodGET,
				ExpectedStatusCodes: []string{statusCodePattern2XX},
				JSONPathAssertions: &AssertionNode{
					Type:     NodeTypeAssertion,
					Path:     "$.status.indicator",
					Operator: "eq",
					Value:    "none",
				},
			}).GetConfig(),
		},
		{
			// Demonstrates followRedirects: false — httpbin.org/status/301
			// always responds with a bare 301 and no Location header, so
			// asserting expectedStatus: 301 here only passes when the
			// redirect itself is surfaced instead of being followed.
			Name:   "Redirect check (no follow)",
			Slug:   "http-redirect-no-follow",
			Period: time.Minute,
			Config: (&HTTPConfig{
				URL:             "https://httpbin.org/status/301",
				Method:          methodGET,
				ExpectedStatus:  sampleRedirectStatus,
				FollowRedirects: boolPtr(false),
			}).GetConfig(),
		},
	}
}

// boolPtr returns a pointer to b, for the presence-aware VerifySsl /
// FollowRedirects config fields.
func boolPtr(b bool) *bool { return &b }
