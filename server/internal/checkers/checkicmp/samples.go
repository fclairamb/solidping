package checkicmp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	sampleCount   = 2               // Number of pings per sample check
	sampleTimeout = 5 * time.Second // Default timeout for sample ping checks
)

// GetSampleConfigs returns sample ICMP check configurations.
func (c *ICMPChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
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
