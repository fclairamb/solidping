package checkmongodb

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample MongoDB check configurations.
func (c *MongoDBChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Local MongoDB",
			Slug:   "mongodb-localhost",
			Period: 5 * time.Minute,
			Config: (&MongoDBConfig{
				Host: "localhost",
				Port: defaultPort,
			}).GetConfig(),
		},
	}
}
