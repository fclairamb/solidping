package config

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	maxTimeout = 30 * time.Second
	minStatus  = 100
	maxStatus  = 599
)

// ValidateSpec validates a check spec offline: it parses the config, applies
// every rule, and fills in the spec defaults (slug) the create path relies on.
func ValidateSpec(spec *checkerdef.CheckSpec) error {
	cfg := &UnixSocketConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Slug == "" {
		spec.Slug = slugFor(cfg.Path)
	}

	return nil
}

// Validate applies every rule to an already-parsed config.
func (c *UnixSocketConfig) Validate() error {
	if err := validatePath(c.Path); err != nil {
		return err
	}

	if c.Timeout != 0 && (c.Timeout < 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf(KeyTimeout, "must be > 0 and <= 30s, got %s", c.Timeout.String())
	}

	if err := c.exchangeFields().Validate(); err != nil {
		return err
	}

	if c.IsHTTPMode() {
		return c.validateHTTPMode()
	}

	if c.ExpectedStatus != 0 {
		return checkerdef.NewConfigError(KeyExpectedStatus, "only applies with http_path")
	}

	if c.BodyExpect != "" {
		return checkerdef.NewConfigError(KeyBodyExpect, "only applies with http_path")
	}

	return nil
}

func (c *UnixSocketConfig) validateHTTPMode() error {
	if c.hasRawFields() {
		return checkerdef.NewConfigError(KeyHTTPPath, "cannot be combined with send_data, expect_data or expect_pattern")
	}

	if !strings.HasPrefix(c.HTTPPath, "/") {
		return checkerdef.NewConfigError(KeyHTTPPath, "must start with /")
	}

	if strings.ContainsAny(c.HTTPPath, " \r\n\x00") {
		return checkerdef.NewConfigError(KeyHTTPPath, "must not contain spaces or control characters")
	}

	if c.ExpectedStatus != 0 && (c.ExpectedStatus < minStatus || c.ExpectedStatus > maxStatus) {
		return checkerdef.NewConfigErrorf(KeyExpectedStatus, "must be between 100 and 599, got %d", c.ExpectedStatus)
	}

	return nil
}

func validatePath(path string) error {
	switch {
	case path == "":
		return checkerdef.NewRequiredConfigError(KeyPath)
	case strings.ContainsRune(path, 0):
		return checkerdef.NewConfigError(KeyPath, "must not contain a NUL byte")
	case strings.HasPrefix(path, "@"):
		return checkerdef.NewConfigError(KeyPath, "abstract sockets are not supported")
	case !filepath.IsAbs(path):
		return checkerdef.NewConfigError(KeyPath, "must be an absolute path")
	case filepath.Clean(path) != path:
		return checkerdef.NewConfigErrorf(KeyPath, "must be a clean path (%s)", filepath.Clean(path))
	}

	return nil
}

// slugFor derives a slug from the socket file name: /run/app/app.sock gives
// unix-socket-app-sock.
func slugFor(path string) string {
	base := strings.ToLower(filepath.Base(path))

	var builder strings.Builder

	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		} else {
			builder.WriteRune('-')
		}
	}

	name := strings.Trim(builder.String(), "-")
	if name == "" {
		return "unix-socket"
	}

	return "unix-socket-" + name
}
