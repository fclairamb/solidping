package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/stretchr/testify/require"
)

// TestWarnDeprecatedFileKeys pins that the aggregation retention deprecation
// warning fires only for a key the operator wrote in a config file. The
// built-in defaults never reach fileKoanf, so a fresh install stays quiet.
func TestWarnDeprecatedFileKeys(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		yaml     string
		wantWarn []string
	}{
		{name: "no config file", yaml: ""},
		{name: "unrelated keys", yaml: "audit:\n  retention_days: 90\n"},
		{
			name:     "deprecated key set",
			yaml:     "aggregation:\n  retention_raw: 48\n",
			wantWarn: []string{"key=aggregation.retention_raw", "value=48"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			fileKoanf := koanf.New(".")

			if tc.yaml != "" {
				path := filepath.Join(t.TempDir(), "config.yml")
				r.NoError(os.WriteFile(path, []byte(tc.yaml), 0o600))
				r.NoError(fileKoanf.Load(file.Provider(path), yaml.Parser()))
			}

			var buf bytes.Buffer
			warnDeprecatedFileKeys(slog.New(slog.NewTextHandler(&buf, nil)), fileKoanf)

			if len(tc.wantWarn) == 0 {
				r.Empty(buf.String())

				return
			}

			r.Contains(buf.String(), "level=WARN")
			for _, want := range tc.wantWarn {
				r.Contains(buf.String(), want)
			}
		})
	}
}
