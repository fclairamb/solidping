package checkdocker

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample Docker check configurations.
func (c *DockerChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Docker PostgreSQL",
			Slug:   "docker-postgres",
			Period: 5 * time.Minute,
			Config: (&DockerConfig{
				ContainerName: "postgres",
			}).GetConfig(),
		},
	}
}
