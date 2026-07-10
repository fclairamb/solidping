package checkrdp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const samplePeriod = 5 * time.Minute

// GetSampleConfigs returns sample RDP check configurations. There is no
// public RDP endpoint to point at, so the samples use placeholder internal
// hosts (RDP servers are typically reachable only from inside a network —
// exactly where solidping's distributed workers run).
func (c *RDPChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Windows Server RDP",
			Slug:   "rdp-server",
			Period: samplePeriod,
			Config: (&RDPConfig{
				Host:       "rdp.example.internal",
				Port:       defaultPort,
				Timeout:    defaultTimeout,
				RequireNLA: true,
			}).GetConfig(),
		},
	}
}
