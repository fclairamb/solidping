package checka2s

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample A2S server check configurations.
func (c *A2SChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "A2S Server",
			Slug:   "a2s-server",
			Period: 5 * time.Minute,
			Config: (&A2SConfig{
				Host: "game.example.com",
				Port: defaultPort,
			}).GetConfig(),
		},
	}
}
