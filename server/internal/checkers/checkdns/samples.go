package checkdns

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	sampleTimeout    = 5 * time.Second // Default timeout for sample DNS checks
	sampleHostGoogle = "google.com"
)

// GetSampleConfigs returns sample DNS check configurations.
func (c *DNSChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
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
	}
}
