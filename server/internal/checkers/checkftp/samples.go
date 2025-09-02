package checkftp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample FTP check configurations.
func (c *FTPChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "FTP Test Server",
			Slug:   "ftp-test-rebex",
			Period: 5 * time.Minute,
			Config: (&FTPConfig{
				Host:     "test.rebex.net",
				Port:     defaultPort,
				Username: "demo",
				Password: "password",
			}).GetConfig(),
		},
	}
}
