package checkdomain

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestDomainChecker_Type(t *testing.T) {
	t.Parallel()

	checker := &DomainChecker{}
	require.Equal(t, checkerdef.CheckTypeDomain, checker.Type())
}

func TestDomainChecker_Validate(t *testing.T) {
	t.Parallel()

	checker := &DomainChecker{}

	tests := []struct {
		name    string
		spec    *checkerdef.CheckSpec
		wantErr bool
	}{
		{
			name: "valid config",
			spec: &checkerdef.CheckSpec{
				Config: map[string]any{
					"domain": "google.com",
				},
			},
			wantErr: false,
		},
		{
			name: "missing domain",
			spec: &checkerdef.CheckSpec{
				Config: map[string]any{},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checker.Validate(tt.spec)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NotEmpty(t, tt.spec.Name)
				require.NotEmpty(t, tt.spec.Slug)
			}
		})
	}
}

func TestDomainChecker_Execute(t *testing.T) {
	t.Parallel()

	// This test performs a real WHOIS lookup
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}

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
