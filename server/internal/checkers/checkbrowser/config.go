package checkbrowser

import (
	"net/url"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	defaultTimeout = 30 * time.Second
	maxTimeout     = 120 * time.Second
)

// BrowserConfig holds the configuration for browser-based health checks.
type BrowserConfig struct {
	URL           string        `json:"url"`
	WaitSelector  string        `json:"waitSelector,omitempty"`
	Keyword       string        `json:"keyword,omitempty"`
	InvertKeyword bool          `json:"invertKeyword,omitempty"`
	Timeout       time.Duration `json:"timeout,omitempty"`
}

// FromMap populates the configuration from a map.
func (c *BrowserConfig) FromMap(configMap map[string]any) error {
	if u, ok := configMap["url"].(string); ok {
		c.URL = u
	} else if configMap["url"] != nil {
		return checkerdef.NewConfigError("url", "must be a string")
	}

	if ws, ok := configMap["waitSelector"].(string); ok {
		c.WaitSelector = ws
	} else if configMap["waitSelector"] != nil {
		return checkerdef.NewConfigError("waitSelector", "must be a string")
	}

	if kw, ok := configMap["keyword"].(string); ok {
		c.Keyword = kw
	} else if configMap["keyword"] != nil {
		return checkerdef.NewConfigError("keyword", "must be a string")
	}

	if ik, ok := configMap["invertKeyword"].(bool); ok {
		c.InvertKeyword = ik
	}

	if t, ok := configMap["timeout"].(string); ok {
		duration, err := time.ParseDuration(t)
		if err != nil {
			return checkerdef.NewConfigError("timeout", "must be a valid duration string")
		}

		c.Timeout = duration
	} else if configMap["timeout"] != nil {
		return checkerdef.NewConfigError("timeout", "must be a string")
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *BrowserConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		"url": c.URL,
	}

	if c.WaitSelector != "" {
		cfg["waitSelector"] = c.WaitSelector
	}

	if c.Keyword != "" {
		cfg["keyword"] = c.Keyword
	}

	if c.InvertKeyword {
		cfg["invertKeyword"] = c.InvertKeyword
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	return cfg
}

// Validate checks if the configuration is valid.
func (c *BrowserConfig) Validate() error {
	if c.URL == "" {
		return checkerdef.NewConfigError("url", "is required")
	}

	if !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://") {
		return checkerdef.NewConfigError("url", "must start with http:// or https://")
	}

	// Reject dangerous URL schemes
	lower := strings.ToLower(c.URL)
	for _, prefix := range []string{"file://", "data:", "javascript:"} {
		if strings.HasPrefix(lower, prefix) {
			return checkerdef.NewConfigError("url", "scheme not allowed")
		}
	}

	if _, err := url.Parse(c.URL); err != nil {
		return checkerdef.NewConfigError("url", "invalid URL format")
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf(
			"timeout", "must be > 0 and <= %s, got %s", maxTimeout, c.Timeout,
		)
	}

	return nil
}

func (c *BrowserConfig) resolveTimeout() time.Duration {
	if c.Timeout != 0 {
		return c.Timeout
	}

	return defaultTimeout
}

func hostnameFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	return parsed.Hostname()
}
