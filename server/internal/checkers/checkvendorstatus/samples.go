package checkvendorstatus

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkvendorstatus/catalog"
)

// GetSampleConfigs returns one sample: the whole GitHub status page.
func (c *VendorStatusChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	github, ok := catalog.Find("github")
	if !ok {
		return nil
	}

	return []checkerdef.CheckSpec{
		{
			Name:   "GitHub status",
			Slug:   "vendor-github",
			Period: 5 * time.Minute,
			Config: (&VendorStatusConfig{
				URL:      github.URL,
				Provider: github.Slug,
			}).GetConfig(),
		},
	}
}
