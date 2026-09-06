package checkssl

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const sampleHostGoogle = "google.com"

// demoHost is the only host the public live demo's SSL catalog inspects:
// our own.
const demoHost = "solidping.io"

// GetSampleConfigs returns sample SSL check configurations.
func (c *SSLChecker) GetSampleConfigs(opts *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	// Demo catalog: our own certificate (spec 2026-09-06-02). A certificate
	// expiry check is the single most legible "why would I want this" example
	// on a demo dashboard, and ours is the one we are entitled to inspect.
	if opts != nil && opts.Type == checkerdef.Demo {
		return []checkerdef.CheckSpec{
			{
				Name:   "solidping.io certificate",
				Slug:   "demo-ssl-solidping",
				Period: 15 * time.Minute,
				Config: (&SSLConfig{Host: demoHost}).GetConfig(),
			},
		}
	}

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
