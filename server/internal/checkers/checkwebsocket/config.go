package checkwebsocket

import (
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	defaultTimeout = 10 * time.Second
	maxTimeout     = 60 * time.Second
)

// WebSocketConfig defines the configuration for WebSocket checks.
type WebSocketConfig struct {
	// URL is the WebSocket endpoint to connect to (must start with ws:// or wss://).
	URL string `json:"url"`

	// Headers is an optional map of HTTP headers to send during the handshake.
	Headers map[string]string `json:"headers,omitempty"`

	// Send is an optional text message to send after connecting.
	Send string `json:"send,omitempty"`

	// Expect is an optional regex pattern to match against the received message.
	Expect string `json:"expect,omitempty"`

	// TLSSkipVerify skips TLS certificate verification when true.
	TLSSkipVerify bool `json:"tls_skip_verify,omitempty"` //nolint:tagliatelle

	// Timeout is the maximum time for the entire check (default: 10s, max: 60s).
	Timeout time.Duration `json:"timeout,omitempty"`
}

// FromMap populates the configuration from a map.
func (c *WebSocketConfig) FromMap(configMap map[string]any) error {
	if urlVal, ok := configMap["url"].(string); ok {
		c.URL = urlVal
	} else if configMap["url"] != nil {
		return checkerdef.NewConfigError("url", "must be a string")
	}

	if headers, ok := configMap["headers"].(map[string]string); ok {
		c.Headers = headers
	} else if headersAny, ok := configMap["headers"].(map[string]any); ok {
		c.Headers = make(map[string]string, len(headersAny))

		for key, val := range headersAny {
			if strVal, ok := val.(string); ok {
				c.Headers[key] = strVal
			} else {
				return checkerdef.NewConfigErrorf("headers", "%s must be a string", key)
			}
		}
	} else if configMap["headers"] != nil {
		return checkerdef.NewConfigError("headers", "must be a map[string]string")
	}

	if send, ok := configMap["send"].(string); ok {
		c.Send = send
	} else if configMap["send"] != nil {
		return checkerdef.NewConfigError("send", "must be a string")
	}

	if expect, ok := configMap["expect"].(string); ok {
		c.Expect = expect
	} else if configMap["expect"] != nil {
		return checkerdef.NewConfigError("expect", "must be a string")
	}

	if tlsSkip, ok := configMap["tls_skip_verify"].(bool); ok {
		c.TLSSkipVerify = tlsSkip
	}

	if timeout, ok := configMap["timeout"].(string); ok {
		duration, err := time.ParseDuration(timeout)
		if err != nil {
			return checkerdef.NewConfigError("timeout", "must be a valid duration string")
		}

		c.Timeout = duration
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *WebSocketConfig) GetConfig() map[string]any {
	config := map[string]any{
		"url": c.URL,
	}

	if len(c.Headers) > 0 {
		config["headers"] = c.Headers
	}

	if c.Send != "" {
		config["send"] = c.Send
	}

	if c.Expect != "" {
		config["expect"] = c.Expect
	}

	if c.TLSSkipVerify {
		config["tls_skip_verify"] = c.TLSSkipVerify
	}

	if c.Timeout != 0 {
		config["timeout"] = c.Timeout.String()
	}

	return config
}

// hostFromURL extracts the hostname from a WebSocket URL.
func hostFromURL(rawURL string) string {
	// Strip scheme
	host := rawURL
	if idx := strings.Index(host, "://"); idx >= 0 {
		host = host[idx+3:]
	}

	// Strip path
	if idx := strings.IndexAny(host, "/?"); idx >= 0 {
		host = host[:idx]
	}

	// Strip port
	if idx := strings.LastIndex(host, ":"); idx >= 0 {
		host = host[:idx]
	}

	return host
}
