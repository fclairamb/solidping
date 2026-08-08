package checkclickhouse

// SecretFields declares which top-level config keys carry secrets and must
// be encrypted at rest. Implements credentials.SecretFielder.
func (c *ClickHouseConfig) SecretFields() []string {
	return []string{fieldPassword}
}
