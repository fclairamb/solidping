package checkworker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	checkbrowserconfig "github.com/fclairamb/solidping/server/internal/checkers/checkbrowser/config"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// TestResolveExtraBudget pins the spec 2026-09-25-35 seam: a browser check
// with capture opted in (or a run carrying a forced/on-demand capture, spec
// 2026-09-25-34) grows the HARD execution deadline by its config's own
// ExtraBudget, while a check with no capture in play, or a config type that
// declares none at all, is unchanged (positive/negative control).
func TestResolveExtraBudget(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	tests := []struct {
		name          string
		config        checkerdef.Config
		forcedCapture bool
		want          time.Duration
	}{
		{
			name:   "browser check with screenshot opted in",
			config: &checkbrowserconfig.BrowserConfig{URL: "https://example.com", Screenshot: true},
			want:   checkbrowserconfig.ScreenshotTimeout,
		},
		{
			name:   "browser check with screenshot NOT opted in",
			config: &checkbrowserconfig.BrowserConfig{URL: "https://example.com"},
			want:   0,
		},
		{
			name:          "browser check forced by an on-demand capture, screenshot not opted in",
			config:        &checkbrowserconfig.BrowserConfig{URL: "https://example.com"},
			forcedCapture: true,
			want:          checkbrowserconfig.ScreenshotTimeout,
		},
		{
			name:   "non-browser config (http) declares no ExtraBudgeter at all",
			config: plainConfig{},
			want:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r.Equal(tt.want, resolveExtraBudget(tt.config, tt.forcedCapture))
		})
	}
}
