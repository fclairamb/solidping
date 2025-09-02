package checkssh

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample SSH check configurations.
func (c *SSHChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "GitHub SSH",
			Slug:   "ssh-github",
			Period: 5 * time.Minute,
			Config: (&SSHConfig{
				Host: "github.com",
				Port: defaultPort,
			}).GetConfig(),
		},
	}
}
