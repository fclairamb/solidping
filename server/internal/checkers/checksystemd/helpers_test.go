package checksystemd_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checksystemd"
)

func sprintf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

// fakeRunner records every call and answers with a fixed output and error.
type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string
	out   string
	err   error
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, append([]string{name}, args...))

	return []byte(f.out), f.err
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.calls)
}

func run(
	t *testing.T, ctx context.Context, runner checksystemd.Runner, cfgMap map[string]any, //nolint:revive // test helper
) *checkerdef.Result {
	t.Helper()

	cfg := &checksystemd.SystemdConfig{}
	require.NoError(t, cfg.FromMap(cfgMap))
	require.NoError(t, cfg.Validate())

	result, err := (&checksystemd.SystemdChecker{Run: runner}).Execute(ctx, cfg)
	require.NoError(t, err)
	require.NotNil(t, result)

	return result
}
