package checkfreeboxline

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Sample-only constants — kept here so threshold defaults can be tweaked
// without touching the active config layer.
const (
	sampleSyncRateDownKbps = 10_000 // 10 Mbps — generous floor for any DSL line
	sampleSnrMarginDb      = 3      // dB — conservative; ANSI calls 6 dB the comfort zone
)

// GetSampleConfigs returns example FreeboxLineConfig configurations. The
// connectionUid is intentionally left blank: the dashboard fills it in
// when the user selects an existing Freebox connection on the form.
func (c *FreeboxLineChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Freebox xDSL line",
			Slug:   "freebox-line-xdsl",
			Period: 5 * time.Minute,
			Config: (&FreeboxLineConfig{
				LinkType:            LinkTypeXDSL,
				MinSyncRateDownKbps: sampleSyncRateDownKbps,
				MinSnrMarginDownDb:  sampleSnrMarginDb,
			}).GetConfig(),
		},
		{
			Name:   "Freebox FTTH line",
			Slug:   "freebox-line-ftth",
			Period: 5 * time.Minute,
			Config: (&FreeboxLineConfig{
				LinkType: LinkTypeFTTH,
			}).GetConfig(),
		},
	}
}
