package systemconfig

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// TestKeyMetricsScrapeTokenIsSecret pins the classification: it must be
// flagged secret so it is stored and rendered like the other credential-shaped
// system parameters (Slack app token, egress-style knobs are the opposite
// case).
func TestKeyMetricsScrapeTokenIsSecret(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for i := range getKnownParameters() {
		def := getKnownParameters()[i]
		if def.Key == KeyMetricsScrapeToken {
			r.True(def.Secret, "metrics.scrape_token must be secret")
			r.Equal(config.EnvMetricsScrapeToken, def.EnvVar)

			return
		}
	}

	t.Fatal("KeyMetricsScrapeToken not registered in getKnownParameters")
}

// TestInitializeAppliesMetricsScrapeToken verifies the DB -> cfg apply path
// (env > db > default) for the /metrics bearer token, mirroring
// TestInitializeAppliesSlackParams. t.Setenv makes the middle case
// non-parallel.
func TestInitializeAppliesMetricsScrapeToken(t *testing.T) {
	tests := []struct {
		name     string
		dbValue  string
		envValue string
		want     string
	}{
		{name: "db value applies to cfg", dbValue: "token-from-db", want: "token-from-db"},
		{name: "env overrides db value", dbValue: "token-from-db", envValue: "token-from-env", want: "token-from-env"},
		{name: "absent params leave config default", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := require.New(t)
			ctx := context.Background()

			if tt.envValue != "" {
				t.Setenv(config.EnvMetricsScrapeToken, tt.envValue)
			}

			dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
			r.NoError(err)
			r.NoError(dbSvc.Initialize(ctx))
			t.Cleanup(func() { _ = dbSvc.Close() })

			if tt.dbValue != "" {
				r.NoError(dbSvc.SetSystemParameter(ctx, string(KeyMetricsScrapeToken), tt.dbValue, true))
			}

			cfg := &config.Config{}
			svc := NewService(dbSvc, cfg)
			r.NoError(svc.Initialize(ctx))

			r.Equal(tt.want, cfg.Prometheus.ScrapeToken)
		})
	}
}
