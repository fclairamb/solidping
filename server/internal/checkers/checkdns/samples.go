package checkdns

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	sampleTimeout          = 5 * time.Second // Default timeout for sample DNS checks
	sampleHostGoogle       = "google.com"
	sampleNameserverGoogle = "8.8.8.8:53" // Google Public DNS resolver (host:port)
	// demoHost is the only name the public live demo's DNS catalogue resolves:
	// our own.
	demoHost = "solidping.io"
)

// GetSampleConfigs returns sample DNS check configurations.
func (c *DNSChecker) GetSampleConfigs(opts *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	// Demo catalogue: resolve our own name, through the region's own resolver
	// (spec 2026-09-06-02). No custom nameserver, so no third party's resolver
	// is queried on the demo's behalf either.
	if opts != nil && opts.Type == checkerdef.Demo {
		return []checkerdef.CheckSpec{
			{
				Name:   "solidping.io A record",
				Slug:   "demo-dns-solidping",
				Period: time.Minute,
				Config: (&DNSConfig{
					Host:       demoHost,
					RecordType: recordTypeA,
					Timeout:    sampleTimeout,
				}).GetConfig(),
			},
		}
	}

	return []checkerdef.CheckSpec{
		{
			Name:   "Google DNS A Record",
			Slug:   "dns-google",
			Period: time.Minute * 5,
			Config: (&DNSConfig{
				Host:       sampleHostGoogle,
				RecordType: recordTypeA,
				Timeout:    sampleTimeout,
			}).GetConfig(),
		},
		{
			Name:   "Cloudflare DNS A Record",
			Slug:   "dns-cloudflare",
			Period: time.Minute * 5,
			Config: (&DNSConfig{
				Host:       "cloudflare.com",
				RecordType: recordTypeA,
				Timeout:    sampleTimeout,
			}).GetConfig(),
		},
		{
			Name:   "GitHub DNS A Record",
			Slug:   "dns-github",
			Period: time.Minute * 5,
			Config: (&DNSConfig{
				Host:       "github.com",
				RecordType: recordTypeA,
				Timeout:    sampleTimeout,
			}).GetConfig(),
		},
		{
			// Exercises the optional custom-resolver path. Nameserver must be
			// host:port (see checker.Validate); a bare IP would fail validation.
			Name:   "Google DNS A via 8.8.8.8",
			Slug:   "dns-google-8888",
			Period: time.Minute * 5,
			Config: (&DNSConfig{
				Host:       sampleHostGoogle,
				RecordType: recordTypeA,
				Nameserver: sampleNameserverGoogle,
				Timeout:    sampleTimeout,
			}).GetConfig(),
		},
	}
}
