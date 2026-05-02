package checkssl

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const sampleHostGoogle = "google.com"

// GetSampleConfigs returns sample SSL check configurations.
func (c *SSLChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "SSL: google.com",
			Slug:   "ssl-google-com",
			Period: 15 * time.Minute,
			Config: (&SSLConfig{Host: sampleHostGoogle}).GetConfig(),
		},
		{
			Name:   "SSL: github.com",
			Slug:   "ssl-github-com",
			Period: 15 * time.Minute,
			Config: (&SSLConfig{Host: "github.com"}).GetConfig(),
		},
	}
}
