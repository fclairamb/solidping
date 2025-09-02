package checkpostgres

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample PostgreSQL check configurations.
func (c *PostgreSQLChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Local PostgreSQL",
			Slug:   "postgresql-localhost",
			Period: 5 * time.Minute,
			Config: (&PostgreSQLConfig{
				Host:     "localhost",
				Port:     defaultPort,
				Username: "postgres",
				Query:    defaultQuery,
			}).GetConfig(),
		},
	}
}
