package checkhost_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhost"
)

// fakeSampler answers with fixed values and counts every read.
type fakeSampler struct {
	calls atomic.Int32

	diskUsed, memUsed, cpu float64
	diskErr, memErr, cpuEr error
}

func (f *fakeSampler) Disk(context.Context, string) (float64, uint64, error) {
	f.calls.Add(1)

	return f.diskUsed, 1000, f.diskErr
}

func (f *fakeSampler) Memory(context.Context) (float64, uint64, error) {
	f.calls.Add(1)

	return f.memUsed, 2000, f.memErr
}

func (f *fakeSampler) CPU(context.Context) (float64, error) {
	f.calls.Add(1)

	return f.cpu, f.cpuEr
}

func run(
	t *testing.T, ctx context.Context, sampler checkhost.Sampler, cfgMap map[string]any, //nolint:revive // test helper
) *checkerdef.Result {
	t.Helper()

	cfg := &checkhost.HostConfig{}
	require.NoError(t, cfg.FromMap(cfgMap))
	require.NoError(t, cfg.Validate())

	result, err := (&checkhost.HostChecker{Sampler: sampler}).Execute(ctx, cfg)
	require.NoError(t, err)
	require.NotNil(t, result)

	return result
}
