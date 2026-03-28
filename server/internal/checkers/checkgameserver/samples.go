package checkgameserver

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample game server check configurations.
func (c *GameServerChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Game Server",
			Slug:   "game-server",
			Period: 5 * time.Minute,
			Config: (&GameServerConfig{
				Host: "game.example.com",
				Port: defaultPort,
			}).GetConfig(),
		},
	}
}
