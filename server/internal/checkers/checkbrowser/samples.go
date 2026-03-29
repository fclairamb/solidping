package checkbrowser

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample browser check configurations.
func (c *BrowserChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Example.com (Browser)",
			Slug:   "browser-example-com",
			Period: time.Hour,
			Config: (&BrowserConfig{
				URL: "https://example.com",
			}).GetConfig(),
		},
	}
}
