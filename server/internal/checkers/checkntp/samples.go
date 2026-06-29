package checkntp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const samplePeriod = 5 * time.Minute

// GetSampleConfigs returns sample NTP check configurations. These are the
// recommended way to monitor a time source (unlike the generic UDP/123
// reachability sample, which sends no NTP packet).
func (c *NTPChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "NTP Pool",
			Slug:   "ntp-pool",
			Period: samplePeriod,
			Config: (&NTPConfig{
				Host:    "pool.ntp.org",
				Timeout: defaultTimeout,
			}).GetConfig(),
		},
		{
			Name:   "Google Time",
			Slug:   "ntp-google",
			Period: samplePeriod,
			Config: (&NTPConfig{
				Host:    "time.google.com",
				Timeout: defaultTimeout,
			}).GetConfig(),
		},
		{
			Name:   "Cloudflare Time",
			Slug:   "ntp-cloudflare",
			Period: samplePeriod,
			Config: (&NTPConfig{
				Host:    "time.cloudflare.com",
				Timeout: defaultTimeout,
			}).GetConfig(),
		},
	}
}
