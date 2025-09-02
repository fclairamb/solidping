package checkudp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	sampleTimeout = 5 * time.Second
)

// GetSampleConfigs returns sample UDP check configurations.
func (c *UDPChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Google DNS (53)",
			Slug:   "udp-google-dns",
			Period: time.Minute * 5,
			Config: (&UDPConfig{
				Host:    "8.8.8.8",
				Port:    53,
				Timeout: sampleTimeout,
			}).GetConfig(),
		},
		{
			Name:   "NTP Pool (123)",
			Slug:   "udp-ntp-pool",
			Period: time.Minute * 5,
			Config: (&UDPConfig{
				Host:    "pool.ntp.org",
				Port:    123,
				Timeout: sampleTimeout,
			}).GetConfig(),
		},
	}
}
