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
		{
			Name:   "RabbitMQ with memory/disk thresholds",
			Slug:   "rabbitmq-management-thresholds",
			Period: 5 * time.Minute,
			Config: (&RabbitMQConfig{
				Host:               "localhost",
				Username:           "guest",
				Password:           "guest",
				Mode:               ModeManagement,
				ManagementPort:     defaultManagementPort,
				MemoryUsedWarning:  "70%",
				MemoryUsedCritical: "90%",
				DiskFreeWarning:    "20GiB",
				DiskFreeCritical:   "5GiB",
			}).GetConfig(),
		},
	}
}
