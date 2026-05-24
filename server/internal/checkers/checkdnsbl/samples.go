package checkdnsbl

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// DNSBL test convention: on most zones 127.0.0.2 is a permanent test-listing
// (always "listed"), while 127.0.0.1 is guaranteed clean (never listed). The
// guaranteed-clean sample below uses the latter so it reports Up reliably.
const (
	sampleTimeout = 10 * time.Second
	// sampleMailServerIP is a documentation IP (RFC 5737) used as a sample target.
	sampleMailServerIP = "203.0.113.10"
)

// GetSampleConfigs returns sample DNSBL check configurations.
func (c *DNSBLChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			// 127.0.0.1 is guaranteed-clean per the DNSBL test convention, so
			// this sample reports Up against the default zones.
			Name:   "DNSBL clean (test IP)",
			Slug:   "dnsbl-clean",
			Period: time.Hour,
			Config: (&DNSBLConfig{
				Target:  "127.0.0.1",
				Timeout: sampleTimeout,
			}).GetConfig(),
		},
		{
			Name:   "Mail server blocklist reputation",
			Slug:   "dnsbl-mail-server",
			Period: time.Hour,
			Config: (&DNSBLConfig{
				Target:  sampleMailServerIP,
				Timeout: sampleTimeout,
			}).GetConfig(),
		},
	}
}
