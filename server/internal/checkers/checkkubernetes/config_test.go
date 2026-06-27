package checkkubernetes_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkkubernetes"
)

func validConfig() *checkkubernetes.KubernetesConfig {
	return &checkkubernetes.KubernetesConfig{
		ClusterUID: "cluster-uid",
		Namespace:  "prod",
		Kind:       checkkubernetes.KindDeployment,
		Name:       "web",
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(c *checkkubernetes.KubernetesConfig)
		wantErr bool
	}{
		{"valid deployment", func(*checkkubernetes.KubernetesConfig) {}, false},
		{"valid replicaset", func(c *checkkubernetes.KubernetesConfig) { c.Kind = checkkubernetes.KindReplicaSet }, false},
		{"empty clusterUid", func(c *checkkubernetes.KubernetesConfig) { c.ClusterUID = "" }, true},
		{"empty namespace", func(c *checkkubernetes.KubernetesConfig) { c.Namespace = "" }, true},
		{"empty name", func(c *checkkubernetes.KubernetesConfig) { c.Name = "" }, true},
		{"bad kind", func(c *checkkubernetes.KubernetesConfig) { c.Kind = "StatefulSet" }, true},
		{"timeout too large", func(c *checkkubernetes.KubernetesConfig) { c.Timeout = 61 * time.Second }, true},
		{"timeout negative", func(c *checkkubernetes.KubernetesConfig) { c.Timeout = -time.Second }, true},
		{"timeout at max", func(c *checkkubernetes.KubernetesConfig) { c.Timeout = 60 * time.Second }, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			cfg := validConfig()
			tc.mutate(cfg)

			err := cfg.Validate()
			if tc.wantErr {
				r.Error(err)
			} else {
				r.NoError(err)
			}
		})
	}
}

func TestConfigFromMapRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	src := map[string]any{
		"clusterUid": "abc",
		"namespace":  "default",
		"kind":       "Deployment",
		"name":       "api",
		"timeout":    "30s",
	}

	cfg := &checkkubernetes.KubernetesConfig{}
	r.NoError(cfg.FromMap(src))
	r.Equal("abc", cfg.ClusterUID)
	r.Equal("default", cfg.Namespace)
	r.Equal("api", cfg.Name)
	r.Equal(30*time.Second, cfg.Timeout)

	out := cfg.GetConfig()
	r.Equal("abc", out["clusterUid"])
	r.Equal("30s", out["timeout"])
}

func TestConfigFromMapTypeErrors(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &checkkubernetes.KubernetesConfig{}
	r.Error(cfg.FromMap(map[string]any{"clusterUid": 123}))
	r.Error(cfg.FromMap(map[string]any{"timeout": "not-a-duration"}))
	r.Error(cfg.FromMap(map[string]any{"timeout": 5}))
}

func TestConfigSecretFieldsEmpty(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Empty(validConfig().SecretFields())
}
