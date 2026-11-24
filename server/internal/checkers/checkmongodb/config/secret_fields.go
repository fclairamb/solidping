package config

// SecretFields declares which top-level config keys carry secrets and must
// be encrypted at rest. Implements credentials.SecretFielder.
func (c *MongoDBConfig) SecretFields() []string {
	return []string{"password"}
}

// TargetFields lists the config keys that decide where the secrets go: a
// browser-sealed blob is bound to them (spec 2026-11-11-01). `username` binds
// too (GHSA-38m7-jhhr-79xx): a sealed password moved to another user fails.
func (c *MongoDBConfig) TargetFields() []string {
	return []string{"host", "port", "database", "username"}
}

// LegacyTargetFields is the list blobs were bound to before `username` was
// added (GHSA-38m7-jhhr-79xx), so the Re-seal page lists them.
func (c *MongoDBConfig) LegacyTargetFields() []string {
	return []string{"host", "port", "database"}
}
