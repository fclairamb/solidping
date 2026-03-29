package checkhttp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	sampleExpectedStatus = 200 // HTTP 200 OK for sample checks
	defaultBaseURL       = "http://localhost:4000"
)

func baseURL(opts *checkerdef.ListSampleOptions) string {
	if opts != nil && opts.BaseURL != "" {
		return opts.BaseURL
	}

	return defaultBaseURL
}

// GetSampleConfigs returns sample HTTP check configurations.
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
					Method:         "GET",
					ExpectedStatus: sampleExpectedStatus,
				}).GetConfig(),
			},
			{
				Name:   "Fake API (Flaky)",
				Slug:   "http-fake-flaky",
				Period: time.Second * 15,
				Config: (&HTTPConfig{
					URL:            base + "/api/v1/fake?period=120",
					Method:         "GET",
					ExpectedStatus: sampleExpectedStatus,
				}).GetConfig(),
			},
			{
				Name:   "Fake API (Unstable)",
				Slug:   "http-fake-unstable",
				Period: time.Second * 15,
				Config: (&HTTPConfig{
					URL:            base + "/api/v1/fake?period=40",
					Method:         "GET",
					ExpectedStatus: sampleExpectedStatus,
				}).GetConfig(),
			},
			{
				Name:   "Fake API (Slow)",
				Slug:   "http-fake-slow",
				Period: time.Second * 20,
				Config: (&HTTPConfig{
					URL:            base + "/api/v1/fake?period=86400&delay=2000",
					Method:         "GET",
					ExpectedStatus: sampleExpectedStatus,
				}).GetConfig(),
			},
			{
				Name:   "Fake API (503 errors)",
				Slug:   "http-fake-503",
				Period: time.Second * 15,
				Config: (&HTTPConfig{
					URL:            base + "/api/v1/fake?period=60&statusDown=503",
					Method:         "GET",
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
				Method:         "GET",
				ExpectedStatus: sampleExpectedStatus,
			}).GetConfig(),
		},
		{
			Name:   "Cloudflare DNS",
			Slug:   "http-cloudflare-dns",
			Period: time.Minute,
			Config: (&HTTPConfig{
				URL:            "https://one.one.one.one",
				Method:         "GET",
				ExpectedStatus: sampleExpectedStatus,
			}).GetConfig(),
		},
	}
}
