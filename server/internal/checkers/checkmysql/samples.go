package checkmysql

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample MySQL check configurations.
func (c *MySQLChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Local MySQL",
			Slug:   "mysql-localhost",
			Period: 5 * time.Minute,
			Config: (&MySQLConfig{
				Host:     "localhost",
				Port:     defaultPort,
				Username: "root",
				Query:    defaultQuery,
			}).GetConfig(),
		},
	}
}
