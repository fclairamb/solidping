package checkredis

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample Redis check configurations.
func (c *RedisChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Local Redis",
			Slug:   "redis-localhost",
			Period: 5 * time.Minute,
			Config: (&RedisConfig{
				Host: "localhost",
				Port: defaultPort,
			}).GetConfig(),
		},
	}
}
