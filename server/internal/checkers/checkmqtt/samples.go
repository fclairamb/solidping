package checkmqtt

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample MQTT check configurations.
func (c *MQTTChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Local MQTT Broker",
			Slug:   "mqtt-localhost",
			Period: 5 * time.Minute,
			Config: (&MQTTConfig{
				Host: "localhost",
				Port: defaultPort,
			}).GetConfig(),
		},
	}
}
