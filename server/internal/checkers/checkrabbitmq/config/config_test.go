package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkrabbitmq"
)

func baseConfig() checkrabbitmq.RabbitMQConfig {
	return checkrabbitmq.RabbitMQConfig{
		Host:     "rabbit.example.com",
		Username: "guest",
		Mode:     checkrabbitmq.ModeManagement,
	}
}

func TestRabbitMQConfig_ThresholdParsing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*checkrabbitmq.RabbitMQConfig)
		wantErr string
	}{
		{
			name: "percent memory thresholds are valid",
			mutate: func(c *checkrabbitmq.RabbitMQConfig) {
				c.MemoryUsedWarning = "70%"
				c.MemoryUsedCritical = "90%"
			},
		},
		{
			name: "byte-size memory thresholds are valid",
			mutate: func(c *checkrabbitmq.RabbitMQConfig) {
				c.MemoryUsedWarning = "1GiB"
				c.MemoryUsedCritical = "1.5GiB"
			},
		},
		{
			name: "numeric byte string is valid",
			mutate: func(c *checkrabbitmq.RabbitMQConfig) {
				c.MemoryUsedCritical = "1610612736"
			},
		},
		{
			name: "byte-size disk thresholds are valid",
			mutate: func(c *checkrabbitmq.RabbitMQConfig) {
				c.DiskFreeWarning = "20GiB"
				c.DiskFreeCritical = "5GiB"
			},
		},
		{
			name: "a single tier alone is valid",
			mutate: func(c *checkrabbitmq.RabbitMQConfig) {
				c.MemoryUsedCritical = "90%"
			},
		},
		{
			name: "invalid memory threshold string",
			mutate: func(c *checkrabbitmq.RabbitMQConfig) {
				c.MemoryUsedCritical = "lots"
			},
			wantErr: "memoryUsedCritical",
		},
		{
			name: "percent out of range is rejected",
			mutate: func(c *checkrabbitmq.RabbitMQConfig) {
				c.MemoryUsedCritical = "150%"
			},
			wantErr: "memoryUsedCritical",
		},
		{
			name: "percent is rejected on a disk key",
			mutate: func(c *checkrabbitmq.RabbitMQConfig) {
				c.DiskFreeCritical = "10%"
			},
			wantErr: "diskFreeCritical",
		},
		{
			name: "memory warning must be lower than critical",
			mutate: func(c *checkrabbitmq.RabbitMQConfig) {
				c.MemoryUsedWarning = "90%"
				c.MemoryUsedCritical = "80%"
			},
			wantErr: "memoryUsedCritical",
		},
		{
			name: "memory tiers equal is rejected (warning must be strictly lower)",
			mutate: func(c *checkrabbitmq.RabbitMQConfig) {
				c.MemoryUsedWarning = "80%"
				c.MemoryUsedCritical = "80%"
			},
			wantErr: "memoryUsedCritical",
		},
		{
			name: "mixed units skip cross-checking",
			mutate: func(c *checkrabbitmq.RabbitMQConfig) {
				c.MemoryUsedWarning = "70%"
				c.MemoryUsedCritical = "1.8GiB"
			},
		},
		{
			name: "disk warning must be greater than critical",
			mutate: func(c *checkrabbitmq.RabbitMQConfig) {
				c.DiskFreeWarning = "5GiB"
				c.DiskFreeCritical = "20GiB"
			},
			wantErr: "diskFreeCritical",
		},
		{
			name: "disk tiers equal is rejected (warning must be strictly greater)",
			mutate: func(c *checkrabbitmq.RabbitMQConfig) {
				c.DiskFreeWarning = "10GiB"
				c.DiskFreeCritical = "10GiB"
			},
			wantErr: "diskFreeCritical",
		},
		{
			name: "threshold requires management mode",
			mutate: func(c *checkrabbitmq.RabbitMQConfig) {
				c.Mode = checkrabbitmq.ModeAMQP
				c.MemoryUsedCritical = "90%"
			},
			wantErr: "memoryUsedCritical",
		},
		{
			name: "threshold requires an explicit mode when the default is amqp",
			mutate: func(c *checkrabbitmq.RabbitMQConfig) {
				c.Mode = ""
				c.MemoryUsedCritical = "90%"
			},
			wantErr: "memoryUsedCritical",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			cfg := baseConfig()
			tt.mutate(&cfg)

			err := cfg.Validate()

			if tt.wantErr == "" {
				r.NoError(err)

				return
			}

			r.Error(err)
			r.Contains(err.Error(), tt.wantErr)
		})
	}
}

func TestRabbitMQConfig_ThresholdRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := baseConfig()
	cfg.MemoryUsedWarning = "70%"
	cfg.MemoryUsedCritical = "1.5GiB"
	cfg.DiskFreeWarning = "20GiB"
	cfg.DiskFreeCritical = "5GiB"

	r.NoError(cfg.Validate())

	m := cfg.GetConfig()
	r.Equal("70%", m["memoryUsedWarning"])
	r.Equal("1.5GiB", m["memoryUsedCritical"])
	r.Equal("20GiB", m["diskFreeWarning"])
	r.Equal("5GiB", m["diskFreeCritical"])

	parsed := &checkrabbitmq.RabbitMQConfig{}
	r.NoError(parsed.FromMap(m))
	r.Equal(cfg.MemoryUsedWarning, parsed.MemoryUsedWarning)
	r.Equal(cfg.MemoryUsedCritical, parsed.MemoryUsedCritical)
	r.Equal(cfg.DiskFreeWarning, parsed.DiskFreeWarning)
	r.Equal(cfg.DiskFreeCritical, parsed.DiskFreeCritical)
}

func TestRabbitMQConfig_ThresholdKeyMustBeString(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &checkrabbitmq.RabbitMQConfig{}
	err := cfg.FromMap(map[string]any{
		"host":              "rabbit.example.com",
		"username":          "guest",
		"memoryUsedWarning": 80,
	})
	r.Error(err)
	r.Contains(err.Error(), "memoryUsedWarning")
}
