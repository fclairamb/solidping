package checkclickhouse

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample ClickHouse check configurations.
func (c *ClickHouseChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Local ClickHouse",
			Slug:   "clickhouse-localhost",
			Period: 5 * time.Minute,
			Config: (&ClickHouseConfig{
				Host:     "localhost",
				Port:     defaultPort,
				Username: defaultUser,
				Database: defaultDatabase,
				Query:    defaultQuery,
			}).GetConfig(),
		},
	}
}
