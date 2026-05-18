package discovery

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScanLocalhost(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	// Scan localhost with only port 4000; we don't require it to be open
	// (the server may not be running), but we do require the scan itself to work.
	cfg := Config{
		CIDRs: []string{"127.0.0.1/32"},
		Ports: []int{80, 443},
	}

	hosts, err := Scan(ctx, cfg)
	r.NoError(err)
	// The scan may return zero hosts if nothing is listening, but it must not error.
	r.NotNil(hosts)
}

func TestScanInvalidCIDR(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	_, err := Scan(ctx, Config{
		CIDRs: []string{"invalid"},
		Ports: []int{80},
	})
	r.Error(err)
}

func TestScanTooLarge(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	_, err := Scan(ctx, Config{
		CIDRs: []string{"10.0.0.0/8"},
		Ports: []int{80},
	})
	r.ErrorIs(err, ErrRangeTooLarge)
}

func TestSortPorts(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	ports := []int{443, 22, 80}
	sortPorts(ports)
	r.Equal([]int{22, 80, 443}, ports)
}
