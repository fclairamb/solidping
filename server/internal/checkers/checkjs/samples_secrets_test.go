package checkjs

import (
	"regexp"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkjs/config"
)

var secretRefRe = regexp.MustCompile(`secrets\.([A-Z_]+)`)

func TestSamplesDeclareTheSecretsTheirScriptReads(t *testing.T) {
	t.Parallel()

	for _, sample := range (&JSChecker{}).GetSampleConfigs(nil) {
		t.Run(sample.Name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.JSConfig{}
			require.NoError(t, cfg.FromMap(sample.Config))

			seen := map[string]bool{}
			for _, m := range secretRefRe.FindAllStringSubmatch(cfg.Script, -1) {
				seen[m[1]] = true
			}
			want := make([]string, 0, len(seen))
			for k := range seen {
				want = append(want, k)
			}
			sort.Strings(want)

			got := make([]string, 0, len(cfg.Secrets))
			for k, v := range cfg.Secrets {
				got = append(got, k)
				assert.Empty(t, v, "sample secret %s must ship empty", k)
			}
			sort.Strings(got)

			assert.Equal(t, want, got)
		})
	}
}

func TestSamplesWithoutSecrets(t *testing.T) {
	t.Parallel()

	for _, sample := range (&JSChecker{}).GetSampleConfigs(nil) {
		if sample.Name == "JS: HTTP Health Check" || sample.Name == "JS: Aggregate Sub-checks" {
			assert.NotContains(t, sample.Config, "secrets", sample.Name)
		}
	}
}
