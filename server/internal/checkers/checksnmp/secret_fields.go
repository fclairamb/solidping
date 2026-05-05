package checksnmp

// SecretFields declares which top-level config keys carry secrets and must
// be encrypted at rest. Implements credentials.SecretFielder.
func (c *SNMPConfig) SecretFields() []string {
	return []string{"authPassword", "privPassword"}
}
