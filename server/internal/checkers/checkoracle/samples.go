package checkoracle

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample Oracle check configurations.
func (c *OracleChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Local Oracle",
			Slug:   "oracle-localhost",
			Period: 5 * time.Minute,
			Config: (&OracleConfig{
				Host:     "localhost",
				Port:     defaultPort,
				Username: "system",
				Query:    defaultQuery,
			}).GetConfig(),
		},
	}
}
