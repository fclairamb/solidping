package entitlements

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestWarnUpgradeTokenFallbackLogsOncePerProcess pins acceptance criterion 3:
// the fallback deprecation warning names the missing parameter and is emitted
// exactly once, no matter how many upgrade URLs get built — adminUpgradeURL sits
// on a dashboard read path, so a per-call log would be pure noise.
//
// Deliberately NOT t.Parallel: it resets the process-wide sync.Once, and Go runs
// sequential tests to completion before any parallel test resumes.
func TestWarnUpgradeTokenFallbackLogsOncePerProcess(t *testing.T) {
	warnUpgradeTokenFallbackOnce = sync.Once{}
	t.Cleanup(func() { warnUpgradeTokenFallbackOnce = sync.Once{} })

	r := require.New(t)
	buf := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	ctx := t.Context()
	for range 5 {
		warnUpgradeTokenFallback(ctx)
	}

	logs := buf.String()
	r.Equal(1, strings.Count(logs, "level=WARN"), "the fallback warning must fire once per process")
	r.Contains(logs, ParamBillingUpgradeTokenSecret)
	r.Contains(logs, "SP_ENTITLEMENTS_BILLING_UPGRADE_TOKEN_SECRET")
}
