package checkcrawl

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns a sample crawl configuration.
func (c *CrawlChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Website crawl",
			Slug:   "website-crawl",
			Period: 24 * time.Hour,
			Config: (&CrawlConfig{URL: "https://www.acme.com/", MaxPages: 100}).GetConfig(),
		},
	}
}
