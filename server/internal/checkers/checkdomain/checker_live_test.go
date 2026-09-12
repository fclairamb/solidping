//go:build slowtests

// This file is behind the `slowtests` build tag because it reaches the PUBLIC
// INTERNET: a real WHOIS/RDAP lookup against a real registrar. It is therefore
// excluded from `make test`, from `make test-postgres` and from both per-PR CI
// jobs, and runs only in the nightly `slowtests` workflow
// (.github/workflows/nightly.yml) or via `make test-slow`.
//
// Splitting it out is what lets the Postgres layer run non-short on every PR:
// `-short` used to conflate "needs a database", "needs the internet" and
// "needs Docker", so dropping -short to get the first one dragged the other two
// — and their flakiness — along with it. See wiki/testing/test-layers.md.

package checkdomain

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestDomainChecker_Execute(t *testing.T) {
	t.Parallel()

	checker := &DomainChecker{}
	ctx := context.Background()

	config := &DomainConfig{
		Domain:        "google.com",
		ThresholdDays: 30,
	}

	result, err := checker.Execute(ctx, config)
	require.NoError(t, err)
	require.NotNil(t, result)

	// google.com should definitely be "up" (not expiring in < 30 days)
	// unless something is very wrong with their registration or the lookup
	require.Equal(t, checkerdef.StatusUp, result.Status)
	require.Contains(t, result.Output, "domain")
	require.Contains(t, result.Output, "expiry_date")
	require.Contains(t, result.Output, "days_remaining")

	daysRemaining, ok := result.Metrics["days_remaining"].(int)
	require.True(t, ok, "days_remaining metric should be an int")
	require.Greater(t, daysRemaining, 30)
}
