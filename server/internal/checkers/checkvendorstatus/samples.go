package checkvendorstatus

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns one sample: the whole GitHub status page.
func (c *VendorStatusChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "GitHub status",
			Slug:   "vendor-github",
			Period: 5 * time.Minute,
			Config: (&VendorStatusConfig{
				URL:      "https://www.githubstatus.com",
				Provider: "github",
			}).GetConfig(),
		},
	}
}
