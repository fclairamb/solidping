package checktcp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	samplePort       = 443             // HTTPS port for sample checks
	sampleTimeout    = 5 * time.Second // Default timeout for sample TCP checks
	sampleHostGoogle = "google.com"
)

// GetSampleConfigs returns sample TCP check configurations.
func (c *TCPChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
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
