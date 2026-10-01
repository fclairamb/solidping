package checkvnc

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	samplePeriod = 5 * time.Minute
	// sampleHost is a placeholder: there is no public VNC endpoint to sample.
	sampleHost = "vnc.example.internal"
)

// GetSampleConfigs returns sample VNC check configurations. VNC servers are
// typically reachable only from inside a network, so the samples use a
// placeholder internal host.
func (c *VNCChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "VNC server",
			Slug:   "vnc-server",
			Period: samplePeriod,
			Config: (&VNCConfig{
				Host:    sampleHost,
				Port:    defaultPort,
				Timeout: defaultTimeout,
			}).GetConfig(),
		},
	}
}
