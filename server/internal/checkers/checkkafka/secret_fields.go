package checkkafka

// SecretFields declares which top-level config keys carry secrets and must
// be encrypted at rest. Implements credentials.SecretFielder.
func (c *KafkaConfig) SecretFields() []string {
	return []string{"saslPassword"}
}
