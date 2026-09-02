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
		{
			// The shape most real gRPC deployments have: TLS on the standard
			// 443, one named service rather than overall server health, and a
			// routing header the edge needs to see.
			Name:   "gRPC service over TLS",
			Slug:   "grpc-service-tls",
			Period: 5 * time.Minute,
			Config: (&GRPCConfig{
				Host:        "grpc.example.com",
				Port:        443,
				TLS:         true,
				ServiceName: "my.service.v1.Greeter",
				Metadata:    map[string]string{"x-tenant": "acme"},
			}).GetConfig(),
		},
	}
}
