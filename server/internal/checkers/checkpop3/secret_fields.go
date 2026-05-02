package checkpop3

// SecretFields declares which top-level config keys carry secrets and must
// be encrypted at rest. Implements credentials.SecretFielder.
func (c *POP3Config) SecretFields() []string {
	return []string{"password"}
}
