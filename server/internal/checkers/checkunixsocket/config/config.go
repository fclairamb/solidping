// Package config holds the unix_socket check's configuration: the struct, its
// map parsing and serialization, and the whole offline rule set
// (ValidateSpec).
//
// It is free of the execution code the parent checkunixsocket package links,
// so `sp checks validate` can run the server's own validators against a
// config-as-code manifest. The parent keeps a type alias.
package config

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Config keys.
const (
	KeyPath           = "path"
	KeyTimeout        = "timeout"
	KeyHTTPPath       = "http_path"
	KeyExpectedStatus = "expected_status"
	KeyBodyExpect     = "body_expect"
	KeySendData       = "send_data"
)

// UnixSocketConfig holds the configuration of a Unix domain socket check.
//
// Three modes share it: connect only (no send, no `http_path`), raw mode
// (`send_data` / `expect_*`, the same exchange as `tcp`) and HTTP mode
// (`http_path`, a GET over the socket).
type UnixSocketConfig struct {
	Path           string        `json:"path"`
	Timeout        time.Duration `json:"timeout,omitempty"`
	SendData       string        `json:"send_data,omitempty"`       //nolint:tagliatelle // API uses snake_case
	SendEncoding   string        `json:"send_encoding,omitempty"`   //nolint:tagliatelle // API uses snake_case
	ExpectData     string        `json:"expect_data,omitempty"`     //nolint:tagliatelle // API uses snake_case
	ExpectEncoding string        `json:"expect_encoding,omitempty"` //nolint:tagliatelle // API uses snake_case
	ExpectPattern  string        `json:"expect_pattern,omitempty"`  //nolint:tagliatelle // API uses snake_case
	HTTPPath       string        `json:"http_path,omitempty"`       //nolint:tagliatelle // API uses snake_case
	ExpectedStatus int           `json:"expected_status,omitempty"` //nolint:tagliatelle // API uses snake_case
	BodyExpect     string        `json:"body_expect,omitempty"`     //nolint:tagliatelle // API uses snake_case
}

// IsHTTPMode reports whether the check sends an HTTP request over the socket.
func (c *UnixSocketConfig) IsHTTPMode() bool {
	return c.HTTPPath != ""
}

// exchangeFields projects the send/expect half of the config onto the shared
// type that parses, serializes and validates it for `tcp` and `udp` too.
func (c *UnixSocketConfig) exchangeFields() *checkerdef.ExchangeFields {
	return &checkerdef.ExchangeFields{
		SendData:       c.SendData,
		SendEncoding:   c.SendEncoding,
		ExpectData:     c.ExpectData,
		ExpectEncoding: c.ExpectEncoding,
		ExpectPattern:  c.ExpectPattern,
	}
}

// hasRawFields reports whether any raw-mode key is set.
func (c *UnixSocketConfig) hasRawFields() bool {
	return c.SendData != "" || c.ExpectData != "" || c.ExpectPattern != ""
}

// FromMap populates the configuration from a map.
func (c *UnixSocketConfig) FromMap(configMap map[string]any) error {
	if err := readString(configMap, KeyPath, &c.Path); err != nil {
		return err
	}

	if raw, ok := configMap[KeyTimeout].(string); ok {
		duration, err := time.ParseDuration(raw)
		if err != nil {
			return checkerdef.NewConfigError(KeyTimeout, "must be a valid duration string")
		}

		c.Timeout = duration
	} else if configMap[KeyTimeout] != nil {
		return checkerdef.NewConfigError(KeyTimeout, "must be a string")
	}

	exchange := checkerdef.ExchangeFields{}
	if err := exchange.FromMap(configMap); err != nil {
		return err
	}

	c.SendData = exchange.SendData
	c.SendEncoding = exchange.SendEncoding
	c.ExpectData = exchange.ExpectData
	c.ExpectEncoding = exchange.ExpectEncoding
	c.ExpectPattern = exchange.ExpectPattern

	if err := readString(configMap, KeyHTTPPath, &c.HTTPPath); err != nil {
		return err
	}

	switch status := configMap[KeyExpectedStatus].(type) {
	case nil:
	case int:
		c.ExpectedStatus = status
	case float64:
		c.ExpectedStatus = int(status)
	default:
		return checkerdef.NewConfigError(KeyExpectedStatus, "must be a number")
	}

	return readString(configMap, KeyBodyExpect, &c.BodyExpect)
}

func readString(configMap map[string]any, key string, target *string) error {
	switch value := configMap[key].(type) {
	case nil:
		return nil
	case string:
		*target = value

		return nil
	default:
		return checkerdef.NewConfigError(key, "must be a string")
	}
}

// GetConfig returns the configuration as a map.
func (c *UnixSocketConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		KeyPath: c.Path,
	}

	if c.Timeout != 0 {
		cfg[KeyTimeout] = c.Timeout.String()
	}

	c.exchangeFields().Apply(cfg)

	if c.HTTPPath != "" {
		cfg[KeyHTTPPath] = c.HTTPPath
	}

	if c.ExpectedStatus != 0 {
		cfg[KeyExpectedStatus] = c.ExpectedStatus
	}

	if c.BodyExpect != "" {
		cfg[KeyBodyExpect] = c.BodyExpect
	}

	return cfg
}

// SchemaNotes implements checkerdef.SchemaNoter: the rules Validate() enforces
// that a schema reflected from this struct cannot state.
func (c *UnixSocketConfig) SchemaNotes() []string {
	return []string{
		"`path` must be an absolute, clean path without a NUL byte. Abstract sockets (`@name`) " +
			"are not supported.",
		"`http_path` (HTTP mode) and the raw-mode keys (`send_data`, `expect_data`, " +
			"`expect_pattern`) are mutually exclusive; with neither set the check only connects.",
		"`expected_status` and `body_expect` apply to HTTP mode only. Without `expected_status`, " +
			"any 2xx or 3xx status is up.",
		"A SaaS shared region never runs this type: it must be pinned to a private location.",
	}
}
