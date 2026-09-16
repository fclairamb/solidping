package checkrabbitmq

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// sampleGuestCredential is RabbitMQ's well-known default account, reused
// across every sample config below.
const sampleGuestCredential = "guest"

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
				Username: sampleGuestCredential,
				Password: sampleGuestCredential,
			}).GetConfig(),
		},
		{
			Name:   "RabbitMQ with memory/disk thresholds",
			Slug:   "rabbitmq-management-thresholds",
			Period: 5 * time.Minute,
			Config: (&RabbitMQConfig{
				Host:               "localhost",
				Username:           sampleGuestCredential,
				Password:           sampleGuestCredential,
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
