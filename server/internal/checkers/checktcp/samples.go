package checktcp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	samplePort       = 443             // HTTPS port for sample checks
	sampleTimeout    = 5 * time.Second // Default timeout for sample TCP checks
	sampleHostGoogle = "google.com"
	// demoHost is the only host the public live demo's TCP catalogue probes:
	// our own.
	demoHost = "solidping.io"
)

// GetSampleConfigs returns sample TCP check configurations.
func (c *TCPChecker) GetSampleConfigs(opts *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	// The public live demo's catalogue probes SOLIDPING-OWNED HOSTS ONLY (spec
	// 2026-09-06-02): it runs from every public region, forever, on a page
	// anyone can open, and that is not bandwidth to spend on a third party.
	if opts != nil && opts.Type == checkerdef.Demo {
		return []checkerdef.CheckSpec{
			{
				Name:   "solidping.io TLS port",
				Slug:   "demo-tcp-solidping",
				Period: time.Minute,
				Config: (&TCPConfig{
					Host:    demoHost,
					Port:    samplePort,
					Timeout: sampleTimeout,
				}).GetConfig(),
			},
		}
	}

	return []checkerdef.CheckSpec{
		{
			Name:   "Google HTTPS (443)",
			Slug:   "tcp-google",
			Period: time.Minute * 5,
			Config: (&TCPConfig{
				Host:    sampleHostGoogle,
				Port:    samplePort,
				Timeout: sampleTimeout,
			}).GetConfig(),
		},
		{
			Name:   "Cloudflare HTTPS (443)",
			Slug:   "tcp-cloudflare",
			Period: time.Minute * 5,
			Config: (&TCPConfig{
				Host:    "cloudflare.com",
				Port:    samplePort,
				Timeout: sampleTimeout,
			}).GetConfig(),
		},
		{
			// Same host as the first sample, pinned to IPv6: the pair is the
			// clearest illustration that one check covers one family.
			Name:   "Google HTTPS over IPv6 (443)",
			Slug:   "tcp-google-ipv6",
			Period: time.Minute * 5,
			Config: checkerdef.SampleConfigWithIPVersion((&TCPConfig{
				Host:    sampleHostGoogle,
				Port:    samplePort,
				Timeout: sampleTimeout,
			}).GetConfig(), checkerdef.IPVersionIPv6),
		},
		{
			Name:   "GitHub HTTPS (443)",
			Slug:   "tcp-github",
			Period: time.Minute * 5,
			Config: (&TCPConfig{
				Host:    "github.com",
				Port:    samplePort,
				Timeout: sampleTimeout,
			}).GetConfig(),
		},
	}
}
