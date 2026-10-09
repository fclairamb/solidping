package notifications

import "errors"

// ErrIntegrationMisconfigured matches (errors.Is) every error a sender returns
// because the integration's own settings are incomplete: a missing token, user
// key, URL or topic. It is not a delivery failure: nothing was sent, and
// retrying cannot help until the settings are fixed. The test endpoint reports
// it with its own code so the dashboard can say what is missing (spec
// 2026-10-08-03).
var ErrIntegrationMisconfigured = errors.New("integration misconfigured")

// ConfigError is a sender error caused by a missing integration setting.
// Setting is the human name of what is missing ("API token").
type ConfigError struct {
	msg     string
	Setting string
}

// Error returns the sender's message, unchanged from the plain sentinel it
// replaced so logs and stored delivery errors read the same.
func (e *ConfigError) Error() string { return e.msg }

// Is makes every ConfigError match ErrIntegrationMisconfigured.
func (e *ConfigError) Is(target error) bool { return target == ErrIntegrationMisconfigured }

func newConfigError(msg, setting string) error {
	return &ConfigError{msg: msg, Setting: setting}
}

// MissingSetting returns the human name of the setting a misconfiguration
// error is about, and whether err is such an error at all.
func MissingSetting(err error) (string, bool) {
	var cfgErr *ConfigError
	if errors.As(err, &cfgErr) {
		return cfgErr.Setting, true
	}

	return "", false
}
