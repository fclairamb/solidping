package checka2s

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample A2S server check configurations.
func (c *A2SChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "A2S Public Server (116.202.55.117)",
			Slug:   "a2s-1",
			Period: 5 * time.Minute,
			Config: (&A2SConfig{
				Host: "116.202.55.117",
				Port: defaultPort,
			}).GetConfig(),
		},
	}
}
