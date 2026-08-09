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
