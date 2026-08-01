package checkerdef

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractTargetHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config map[string]any
		want   *string
	}{
		{
			name:   "http with url",
			config: map[string]any{"url": "https://api.example.com/health"},
			want:   strPtr("api.example.com"),
		},
		{
			name:   "http url with port keeps hostname only",
			config: map[string]any{"url": "https://api.example.com:8443/health"},
			want:   strPtr("api.example.com"),
		},
		{
			name:   "tcp with host+port",
			config: map[string]any{"host": "db.internal.example.com", "port": float64(5432)},
			want:   strPtr("db.internal.example.com"),
		},
		{
			name:   "ssl with host",
			config: map[string]any{"host": "secure.example.com"},
			want:   strPtr("secure.example.com"),
		},
		{
			name:   "dnsbl with target",
			config: map[string]any{"target": "1.2.3.4"},
			want:   strPtr("1.2.3.4"),
		},
		{
			name:   "host takes priority over url",
			config: map[string]any{"host": "from-host.example.com", "url": "https://from-url.example.com"},
			want:   strPtr("from-host.example.com"),
		},
		{
			name:   "url takes priority over target",
			config: map[string]any{"url": "https://from-url.example.com", "target": "from-target.example.com"},
			want:   strPtr("from-url.example.com"),
		},
		{
			name:   "heartbeat (token only) → null",
			config: map[string]any{"token": "abc123"},
			want:   nil,
		},
		{
			name:   "empty config → null",
			config: map[string]any{},
			want:   nil,
		},
		{
			name:   "nil config → null",
			config: nil,
			want:   nil,
		},
		{
			name:   "empty string host falls through to url",
			config: map[string]any{"host": "", "url": "https://fallback.example.com"},
			want:   strPtr("fallback.example.com"),
		},
		{
			name:   "non-string host value ignored",
			config: map[string]any{"host": 12345, "target": "fallback-target.example.com"},
			want:   strPtr("fallback-target.example.com"),
		},
		{
			name:   "unparseable url with no host/target → null",
			config: map[string]any{"url": "://missing-scheme"},
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			got := ExtractTargetHost(tt.config)
			if tt.want == nil {
				r.Nil(got)
			} else {
				r.NotNil(got)
				r.Equal(*tt.want, *got)
			}
		})
	}
}

func strPtr(s string) *string {
	return &s
}
