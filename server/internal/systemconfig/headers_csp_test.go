package systemconfig

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// TestKeyHeadersCSPExtraSourcesRegistered pins the parameter's env binding
// (through the systemconfig EnvVar mechanism, since the snake_case key is
// unreachable by koanf's env loader) and its non-secret classification.
func TestKeyHeadersCSPExtraSourcesRegistered(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	envVar, ok := EnvVarForKey(string(KeyHeadersCSPExtraSources))
	r.True(ok, "headers.csp_extra_sources must be a known system parameter")
	r.Equal(config.EnvHeadersCSPExtraSources, envVar)
	r.Contains(KnownEnvVars(), "SP_HEADERS_CSP_EXTRA_SOURCES")

	for _, def := range getKnownParameters() {
		if def.Key == KeyHeadersCSPExtraSources {
			r.False(def.Secret)
		}
	}
}

// TestInitializeAppliesHeadersCSPExtraSources verifies env > db > default for
// the operator CSP relaxation knob. t.Setenv makes the env case non-parallel.
func TestInitializeAppliesHeadersCSPExtraSources(t *testing.T) {
	tests := []struct {
		name     string
		dbValue  string
		envValue string
		want     string
	}{
		{name: "db value applies to cfg", dbValue: " img-src https://cdn.acme.com ", want: "img-src https://cdn.acme.com"},
		{
			name: "env overrides db value", dbValue: "img-src https://cdn.acme.com",
			envValue: "font-src https://fonts.acme.com", want: "font-src https://fonts.acme.com",
		},
		{name: "absent params leave config default", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := require.New(t)
			ctx := context.Background()

			if tt.envValue != "" {
				t.Setenv(config.EnvHeadersCSPExtraSources, tt.envValue)
			}

			dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
			r.NoError(err)
			r.NoError(dbSvc.Initialize(ctx))
			t.Cleanup(func() { _ = dbSvc.Close() })

			if tt.dbValue != "" {
				r.NoError(dbSvc.SetSystemParameter(ctx, string(KeyHeadersCSPExtraSources), tt.dbValue, false))
			}

			cfg := &config.Config{}
			svc := NewService(dbSvc, cfg)
			r.NoError(svc.Initialize(ctx))

			r.Equal(tt.want, cfg.Headers.CSPExtraSources)
		})
	}
}
