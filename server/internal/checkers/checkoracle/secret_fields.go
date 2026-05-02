package checkoracle

// SecretFields declares which top-level config keys carry secrets and must
// be encrypted at rest. Implements credentials.SecretFielder.
func (c *OracleConfig) SecretFields() []string {
	return []string{"password"}
}
