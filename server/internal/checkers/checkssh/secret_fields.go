package checkssh

// SecretFields declares which top-level config keys carry secrets and must
// be encrypted at rest. Implements credentials.SecretFielder.
func (c *SSHConfig) SecretFields() []string {
	return []string{"password", "private_key"}
}
