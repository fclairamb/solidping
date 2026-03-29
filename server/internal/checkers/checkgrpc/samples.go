package checkgrpc

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample gRPC check configurations.
func (c *GRPCChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Local gRPC",
			Slug:   "grpc-localhost",
			Period: 5 * time.Minute,
			Config: (&GRPCConfig{
				Host: "localhost",
				Port: defaultPort,
			}).GetConfig(),
		},
	}
}
