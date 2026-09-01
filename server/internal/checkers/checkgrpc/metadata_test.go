package checkgrpc_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkgrpc"
)

// A health endpoint behind an authenticating proxy needs request metadata.
// This asserts the EXACT keys and values reach the server — the check is
// worthless if the metadata is silently dropped, and "the check is up" would
// not have caught that on a server that does not enforce the header.
func TestMetadataReachesTheServer(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := startTestServer(t, testServerOptions{})

	cfg := srv.baseConfig()
	cfg["metadata"] = map[string]any{"x-tenant": "acme", "x-trace": "probe-1"}
	cfg["secretMetadata"] = map[string]any{"authorization": "Bearer s3cr3t-token"}

	result := execute(t, cfg)
	r.Equal(checkerdef.StatusUp, result.Status)

	received := srv.lastMetadata(t)
	r.Equal([]string{"acme"}, received.Get("x-tenant"))
	r.Equal([]string{"probe-1"}, received.Get("x-trace"))
	r.Equal([]string{"Bearer s3cr3t-token"}, received.Get("authorization"))
}

// The secret half must never come back out in a result. Serializing the whole
// output is what makes this a real assertion rather than a check of the one key
// we happened to think of.
func TestSecretMetadataNeverAppearsInOutput(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	const secretValue = "Bearer s3cr3t-token"

	srv := startTestServer(t, testServerOptions{})

	cfg := srv.baseConfig()
	cfg["secretMetadata"] = map[string]any{"authorization": secretValue}
	// A failing service exercises the error-rendering paths too, which are
	// where a leak would most plausibly hide.
	cfg["serviceName"] = "never.registered.v1"

	result := execute(t, cfg)

	encoded, err := json.Marshal(result.Output)
	r.NoError(err)
	r.NotContains(string(encoded), secretValue)
	r.NotContains(string(encoded), "authorization")
}

// Secret values win a collision: the explicit credential is what the operator
// meant, and letting the public half override it would be a silent downgrade.
func TestSecretMetadataOverridesThePlainValue(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := startTestServer(t, testServerOptions{})

	cfg := srv.baseConfig()
	cfg["metadata"] = map[string]any{"authorization": "placeholder"}
	cfg["secretMetadata"] = map[string]any{"authorization": "Bearer real"}

	r.Equal(checkerdef.StatusUp, execute(t, cfg).Status)
	r.Equal([]string{"Bearer real"}, srv.lastMetadata(t).Get("authorization"))
}

func TestMetadataRoundTripsThroughTheConfigMap(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	config := &checkgrpc.GRPCConfig{}
	r.NoError(config.FromMap(map[string]any{
		"host":           "grpc.example.com",
		"metadata":       map[string]any{"x-tenant": "acme"},
		"secretMetadata": map[string]any{"authorization": "Bearer t"},
	}))

	r.Equal(map[string]string{"x-tenant": "acme"}, config.Metadata)
	r.Equal(map[string]string{"authorization": "Bearer t"}, config.SecretMetadata)

	out := config.GetConfig()
	r.Equal(map[string]string{"x-tenant": "acme"}, out["metadata"])
	r.Equal(map[string]string{"authorization": "Bearer t"}, out["secretMetadata"])

	// An empty map writes no key at all, so an untouched config never grows one.
	empty := &checkgrpc.GRPCConfig{Host: "h"}
	r.NotContains(empty.GetConfig(), "metadata")
	r.NotContains(empty.GetConfig(), "secretMetadata")
}

// secretMetadata must be declared secret so it is split out of the public,
// queryable config column and encrypted at rest.
func TestSecretMetadataIsDeclaredSecret(t *testing.T) {
	t.Parallel()

	config := &checkgrpc.GRPCConfig{}
	require.Equal(t, []string{"secretMetadata"}, config.SecretFields())
}

func TestMetadataKeyValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     string
		wantErr string
	}{
		{name: "plain key", key: "x-tenant"},
		{name: "dotted key", key: "acme.tenant"},
		{name: "underscored key", key: "x_tenant"},
		{
			name:    "uppercase is rejected rather than silently lowercased",
			key:     "X-Tenant",
			wantErr: "is invalid",
		},
		{
			name:    "the grpc- prefix is reserved by the runtime",
			key:     "grpc-timeout",
			wantErr: "reserved",
		},
		{
			name:    "binary metadata cannot be expressed as a plain string",
			key:     "trace-bin",
			wantErr: "binary metadata",
		},
		{
			name:    "spaces are not header characters",
			key:     "x tenant",
			wantErr: "is invalid",
		},
		{
			name:    "an empty key is rejected",
			key:     "",
			wantErr: "must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			// Both maps enforce the same rules — assert each independently so a
			// rule applied to only one of them fails here.
			for _, key := range []string{"metadata", "secretMetadata"} {
				config := &checkgrpc.GRPCConfig{}
				r.NoError(config.FromMap(map[string]any{
					"host": "grpc.example.com",
					key:    map[string]any{tt.key: "v"},
				}))

				err := config.Validate()
				if tt.wantErr == "" {
					r.NoErrorf(err, "%s key %q must be accepted", key, tt.key)

					continue
				}

				r.Errorf(err, "%s key %q must be rejected", key, tt.key)
				r.Contains(err.Error(), tt.wantErr)
			}
		})
	}
}

// A non-string metadata value is a config error, not a panic or a silent drop.
func TestMetadataRejectsNonStringValues(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	config := &checkgrpc.GRPCConfig{}
	err := config.FromMap(map[string]any{
		"host":     "grpc.example.com",
		"metadata": map[string]any{"x-tenant": 42},
	})

	r.Error(err)
	r.Contains(err.Error(), "must be a string")
}
