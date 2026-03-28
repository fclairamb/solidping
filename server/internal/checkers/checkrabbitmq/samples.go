package checkrabbitmq

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample RabbitMQ check configurations.
func (c *RabbitMQChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Local RabbitMQ",
			Slug:   "rabbitmq-localhost",
			Period: 5 * time.Minute,
			Config: (&RabbitMQConfig{
				Host:     "localhost",
				Port:     defaultPort,
				Username: "guest",
				Password: "guest",
			}).GetConfig(),
		},
	}
}
