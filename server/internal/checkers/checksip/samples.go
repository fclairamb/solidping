package checksip

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	sampleTimeout = 5 * time.Second
	samplePBXHost = "pbx.example.com"
)

// GetSampleConfigs returns sample SIP check configurations: an OPTIONS
// reachability probe and a REGISTER auth probe with placeholder credentials.
func (c *SIPChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "SIP OPTIONS reachability",
			Slug:   "sip-options",
			Period: time.Minute,
			Config: (&SIPConfig{
				Host:      "sip.antisip.com",
				Port:      defaultPortPlain,
				Transport: transportUDP,
				Mode:      modeOptions,
				Timeout:   sampleTimeout,
			}).GetConfig(),
		},
		{
			Name:   "SIP REGISTER auth",
			Slug:   "sip-register",
			Period: time.Minute,
			Config: (&SIPConfig{
				Host:      samplePBXHost,
				Port:      defaultPortPlain,
				Transport: transportUDP,
				Mode:      modeRegister,
				Domain:    samplePBXHost,
				Username:  "1001",
				Password:  "change-me",
				Timeout:   sampleTimeout,
			}).GetConfig(),
		},
	}
}
