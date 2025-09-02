package checksftp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample SFTP check configurations.
func (c *SFTPChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "SFTP Test Server",
			Slug:   "sftp-test-rebex",
			Period: 5 * time.Minute,
			Config: (&SFTPConfig{
				Host:     "test.rebex.net",
				Port:     defaultPort,
				Username: "demo",
				Password: "password",
			}).GetConfig(),
		},
	}
}
