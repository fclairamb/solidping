package checkbrowser

import (
	"net/url"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	defaultTimeout = 30 * time.Second
	maxTimeout     = 30 * time.Second
)

// BrowserConfig holds the configuration for browser-based health checks.
type BrowserConfig struct {
	URL           string        `json:"url"`
	WaitSelector  string        `json:"waitSelector,omitempty"`
	Keyword       string        `json:"keyword,omitempty"`
	InvertKeyword bool          `json:"invertKeyword,omitempty"`
	Timeout       time.Duration `json:"timeout,omitempty"`
	// Screenshot opts this check into capturing a PNG of the page when the
	// execution FAILS (spec 2026-08-21-01). Default false, and deliberately so:
	// a capture costs a CDP round-trip and up to a few MiB of memory on the
	// most expensive check type there is, and most operators never need it.
	//
	// Opting in changes storage, never verdicts — the capture is time-boxed and
	// every failure path drops it silently (see captureScreenshot).
	Screenshot bool `json:"screenshot,omitempty"`
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

	if shot, ok := configMap["screenshot"].(bool); ok {
		c.Screenshot = shot
	} else if configMap["screenshot"] != nil {
		return checkerdef.NewConfigError("screenshot", "must be a boolean")
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

	if c.Screenshot {
		cfg["screenshot"] = c.Screenshot
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	return cfg
}

// ValidateNavigationURL applies the browser check's URL rules to any URL a
// browser is asked to navigate to: `http`/`https` only, `file:`/`data:`/
// `javascript:` refused, parseable.
//
// Exported because a JS script's `page.goto(url)` must be held to EXACTLY the
// same rules as a browser check's `url` field — one rule set, one place to
// change it, no way for the scripted path to reach a scheme the configured
// path refuses (spec 2026-09-12-06 §1).
func ValidateNavigationURL(rawURL string) error {
	if rawURL == "" {
		return checkerdef.NewConfigError("url", "is required")
	}

	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return checkerdef.NewConfigError("url", "must start with http:// or https://")
	}

	// Reject dangerous URL schemes
	lower := strings.ToLower(rawURL)
	for _, prefix := range []string{"file://", "data:", "javascript:"} {
		if strings.HasPrefix(lower, prefix) {
			return checkerdef.NewConfigError("url", "scheme not allowed")
		}
	}

	if _, err := url.Parse(rawURL); err != nil {
		return checkerdef.NewConfigError("url", "invalid URL format")
	}

	return nil
}

// Validate checks if the configuration is valid.
func (c *BrowserConfig) Validate() error {
	if err := ValidateNavigationURL(c.URL); err != nil {
		return err
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
