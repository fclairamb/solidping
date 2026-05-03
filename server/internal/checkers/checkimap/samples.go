package checkimap

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample IMAP check configurations.
func (c *IMAPChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Gmail IMAP",
			Slug:   "imap-gmail",
			Period: 5 * time.Minute,
			Config: (&IMAPConfig{
				Host: "imap.gmail.com",
				Port: implicitTLSPort,
				TLS:  true,
			}).GetConfig(),
		},
	}
}
