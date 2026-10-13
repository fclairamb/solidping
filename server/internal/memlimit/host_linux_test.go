//go:build linux

package memlimit

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseMemTotal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		in     string
		want   int64
		wantOK bool
	}{
		{"pi 1GB", "MemTotal:         948304 kB\nMemFree:  1 kB\n", 948304 * 1024, true},
		{"second line", "Foo: 1 kB\nMemTotal: 2048 kB\n", 2048 * 1024, true},
		{"missing", "MemFree: 1 kB\n", 0, false},
		{"garbage", "MemTotal: abc kB\n", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseMemTotal([]byte(tt.in))
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}
}
