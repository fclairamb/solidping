package config

// SecretFields declares which top-level config keys carry secrets and must
// be encrypted at rest. Implements credentials.SecretFielder.
func (c *MySQLConfig) SecretFields() []string {
	return []string{"password"}
}

// TargetFields lists the config keys that decide where the secrets go: a
// browser-sealed blob is bound to them (spec 2026-11-11-01). The Unix socket
// binds too: a socket config leaves the network target empty or fixed, so
// without it a sealed blob could be pointed at another socket (spec
// 2026-11-24-10).
func (c *MySQLConfig) TargetFields() []string {
	return append(c.LegacyTargetFields(), configKeySocket)
}

// LegacyTargetFields is the list blobs were bound to before the Unix socket
// was a target field (spec 2026-11-24-10).
func (c *MySQLConfig) LegacyTargetFields() []string {
	return []string{"host", "port", "database"}
}
