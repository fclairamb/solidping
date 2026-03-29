package checkmssql

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample MSSQL check configurations.
func (c *MSSQLChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Local MSSQL",
			Slug:   "mssql-localhost",
			Period: 5 * time.Minute,
			Config: (&MSSQLConfig{
				Host:     "localhost",
				Port:     defaultPort,
				Username: "sa",
				Query:    defaultQuery,
			}).GetConfig(),
		},
	}
}
