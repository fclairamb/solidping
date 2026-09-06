package checkicmp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	sampleCount   = 2               // Number of pings per sample check
	sampleTimeout = 5 * time.Second // Default timeout for sample ping checks
	// demoHost is the only host the public live demo's ICMP catalog pings:
	// our own.
	demoHost = "solidping.io"
)

// GetSampleConfigs returns sample ICMP check configurations.
func (c *ICMPChecker) GetSampleConfigs(opts *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	// Demo catalog: our own host only (spec 2026-09-06-02). An ICMP probe is
	// cheap, but "cheap" times every public region times forever is still
	// somebody else's traffic if the target is not ours.
	if opts != nil && opts.Type == checkerdef.Demo {
		return []checkerdef.CheckSpec{
			{
				Name:   "Ping solidping.io",
				Slug:   "demo-icmp-solidping",
				Period: time.Minute,
				Config: (&ICMPConfig{
					Host:    demoHost,
					Count:   sampleCount,
					Timeout: sampleTimeout,
				}).GetConfig(),
			},
		}
	}

	return []checkerdef.CheckSpec{
		{
			Name:   "Google DNS (8.8.8.8)",
			Slug:   "icmp-google-dns",
			Period: time.Minute * 5,
			Config: (&ICMPConfig{
				Host:    "8.8.8.8",
				Count:   sampleCount,
				Timeout: sampleTimeout,
			}).GetConfig(),
		},
		{
			Name:   "Cloudflare DNS (1.1.1.1)",
			Slug:   "icmp-cloudflare",
			Period: time.Minute * 5,
			Config: (&ICMPConfig{
				Host:    "1.1.1.1",
				Count:   sampleCount,
				Timeout: sampleTimeout,
			}).GetConfig(),
		},
		{
			// The reason the ipVersion option exists, as a ready-made example:
			// an IPv4 ping proves nothing about the IPv6 path, so verifying
			// IPv6 reachability means a check pinned to it.
			Name:   "Cloudflare DNS over IPv6",
			Slug:   "icmp-cloudflare-ipv6",
			Period: time.Minute * 5,
			Config: checkerdef.SampleConfigWithIPVersion((&ICMPConfig{
				Host:    "one.one.one.one",
				Count:   sampleCount,
				Timeout: sampleTimeout,
			}).GetConfig(), checkerdef.IPVersionIPv6),
		},
		{
			Name:   "Google DNS Alt (8.8.4.4)",
			Slug:   "icmp-google-alt",
			Period: time.Minute * 5,
			Config: (&ICMPConfig{
				Host:    "8.8.4.4",
				Count:   sampleCount,
				Timeout: sampleTimeout,
			}).GetConfig(),
		},
	}
}
