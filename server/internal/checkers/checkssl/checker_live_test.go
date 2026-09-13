//go:build slowtests

// This file is behind the `slowtests` build tag because it dials a real host on
// the PUBLIC INTERNET to read its certificate. Excluded from `make test`, from
// `make test-postgres` and from both per-PR CI jobs; runs in the nightly
// `slowtests` workflow or via `make test-slow`.
//
// Same reason as internal/checkers/checkdomain/checker_live_test.go: the
// Postgres layer can only run non-short on every PR once "needs the internet"
// stops sharing a switch with "needs a database".
// See wiki/testing/test-layers.md.

package checkssl

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestSSLChecker_Execute(t *testing.T) {
	t.Parallel()

	checker := &SSLChecker{}
	ctx := context.Background()

	config := &SSLConfig{
		Host:          "google.com",
		ThresholdDays: 7,
	}

	result, err := checker.Execute(ctx, config)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, checkerdef.StatusUp, result.Status)

	require.Contains(t, result.Output, "subject")
	require.Contains(t, result.Output, "issuer")
	require.Contains(t, result.Output, "not_after")
	require.Contains(t, result.Output, "days_remaining")
	require.Contains(t, result.Output, "tls_version")
	require.Contains(t, result.Output, "dns_names")

	daysRemaining, ok := result.Metrics["days_remaining"].(int)
	require.True(t, ok, "days_remaining metric should be an int")
	require.Greater(t, daysRemaining, 7)
}
