package checkkafka

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample Kafka check configurations.
func (c *KafkaChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Local Kafka",
			Slug:   "kafka-localhost",
			Period: 5 * time.Minute,
			Config: (&KafkaConfig{
				Brokers: []string{"localhost:9092"},
			}).GetConfig(),
		},
	}
}
