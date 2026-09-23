package config

import (
	"net/url"
	"strings"
)

// defaultSlug derives `prometheus-<host>` from the target URL, falling back to
// a bare `prometheus` when the host cannot be read.
func defaultSlug(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return "prometheus"
	}

	host := strings.NewReplacer(".", "-", ":", "-").Replace(parsed.Hostname())

	return "prometheus-" + host
}
