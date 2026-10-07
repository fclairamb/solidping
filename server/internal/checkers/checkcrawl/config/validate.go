package config

import (
	"net/url"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ValidateSpec validates a crawl check spec offline: it parses the config,
// applies every rule and fills in the spec defaults (name, slug).
func ValidateSpec(spec *checkerdef.CheckSpec) error {
	if err := ValidateMaxPagesPresent(spec.Config); err != nil {
		return err
	}

	cfg := &CrawlConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	host := cfg.URL
	if parsed, err := url.Parse(cfg.URL); err == nil {
		host = parsed.Host
	}

	if spec.Name == "" {
		spec.Name = "Crawl: " + host
	}

	if spec.Slug == "" {
		spec.Slug = "crawl-" + strings.NewReplacer(".", "-", ":", "-").Replace(host)
	}

	return nil
}
