package config

// SecretFields declares which top-level config keys carry secrets and must
// be encrypted at rest. Implements credentials.SecretFielder.
func (c *SFTPConfig) SecretFields() []string {
	return []string{"password", "private_key"}
}

// TargetFields lists the config keys that decide where the secrets go: a
// browser-sealed blob is bound to them (spec 2026-11-11-01).
func (c *SFTPConfig) TargetFields() []string {
	return []string{"host", "port"}
}
