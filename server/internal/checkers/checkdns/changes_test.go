package checkdns

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkdns/config"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// stubChecker answers every lookup with values (or err) instead of querying DNS.
func stubChecker(values []string, err error) *DNSChecker {
	return &DNSChecker{lookup: func(
		_ context.Context, _ *DNSConfig, recordType string, _ time.Duration,
	) ([]string, []string, error) {
		if err != nil {
			return nil, nil, err
		}

		if recordType == recordTypeA || recordType == recordTypeAAAA {
			return values, nil, nil
		}

		return nil, values, nil
	}}
}

func nsConfig(baseline map[string][]string) *DNSConfig {
	cfg := &DNSConfig{
		Host:          "acme.com",
		RecordType:    recordTypeNS,
		DetectChanges: true,
		Baseline:      baseline,
	}
	cfg.SelectRegion("eu-west")

	return cfg
}

func TestChanges_NoBaselineCaptures(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	res, err := stubChecker([]string{"NS2.acme.com", "ns1.acme.com."}, nil).
		Execute(context.Background(), nsConfig(nil))
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, res.Status)
	r.Equal([]string{"ns1.acme.com", "ns2.acme.com"}, res.Output[OutputKeyBaselineCapture])
	r.NotContains(res.Output, OutputKeyChanges)
	r.Equal(0, res.Metrics[MetricChanged])
}

func TestChanges_FailedLookupNeverCaptures(t *testing.T) {
	t.Parallel()

	t.Run("nxdomain", func(t *testing.T) {
		t.Parallel()

		res, err := stubChecker(nil, &net.DNSError{Err: "no such host", Name: "acme.com", IsNotFound: true}).
			Execute(context.Background(), nsConfig(nil))
		require.NoError(t, err)
		require.Equal(t, checkerdef.StatusDown, res.Status)
		require.NotContains(t, res.Output, OutputKeyBaselineCapture)
	})

	t.Run("timeout", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancel()

		<-ctx.Done()

		res, err := stubChecker(nil, context.DeadlineExceeded).Execute(ctx, nsConfig(nil))
		require.NoError(t, err)
		require.Equal(t, checkerdef.StatusTimeout, res.Status)
		require.NotContains(t, res.Output, OutputKeyBaselineCapture)
	})

	t.Run("empty answer", func(t *testing.T) {
		t.Parallel()

		res, err := stubChecker([]string{}, nil).Execute(context.Background(), nsConfig(nil))
		require.NoError(t, err)
		require.NotContains(t, res.Output, OutputKeyBaselineCapture)
	})
}

func TestChanges_SameValuesDifferentShapeIsNoChange(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := nsConfig(map[string][]string{"eu-west": {"ns1.acme.com", "ns2.acme.com"}})

	res, err := stubChecker([]string{"NS2.ACME.com.", " ns1.acme.com"}, nil).Execute(context.Background(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, res.Status)
	r.NotContains(res.Output, OutputKeyChanges)
	r.NotContains(res.Output, OutputKeyBaselineCapture)
	r.Equal(0, res.Metrics[MetricChanged])
}

func TestChanges_AddedAndRemoved(t *testing.T) {
	t.Parallel()

	baseline := map[string][]string{"eu-west": {"ns1.acme.com", "ns2.acme.com"}}

	t.Run("added", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		res, err := stubChecker([]string{"ns1.acme.com", "ns2.acme.com", "ns3.evil.com"}, nil).
			Execute(context.Background(), nsConfig(baseline))
		r.NoError(err)
		r.Equal(checkerdef.StatusDown, res.Status)
		r.Equal(map[string]any{"added": []string{"ns3.evil.com"}, "removed": []string{}}, res.Output[OutputKeyChanges])
		r.Equal("DNS records changed: +1 −0", res.Output[checkerdef.OutputKeyError])
		r.Equal(1, res.Metrics[MetricChanged])
	})

	t.Run("removed", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		res, err := stubChecker([]string{"ns1.acme.com"}, nil).Execute(context.Background(), nsConfig(baseline))
		r.NoError(err)
		r.Equal(checkerdef.StatusDown, res.Status)
		r.Equal(map[string]any{"added": []string{}, "removed": []string{"ns2.acme.com"}}, res.Output[OutputKeyChanges])
	})
}

func TestChanges_OnChangeWarning(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := nsConfig(map[string][]string{"eu-west": {"ns1.acme.com"}})
	cfg.OnChange = checkconfig.OnChangeWarning

	res, err := stubChecker([]string{"ns9.acme.com"}, nil).Execute(context.Background(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusWarning, res.Status)
	r.Contains(res.Output, OutputKeyChanges)
	r.NotContains(res.Output, checkerdef.OutputKeyError)
}

func TestChanges_ExpectedValuesErrorKept(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := nsConfig(map[string][]string{"eu-west": {"ns1.acme.com"}})
	cfg.ExpectedValues = []string{"ns1.acme.com"}

	res, err := stubChecker([]string{"ns2.acme.com"}, nil).Execute(context.Background(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusDown, res.Status)
	r.Equal("resolved values do not match expected values", res.Output[checkerdef.OutputKeyError])
	r.Contains(res.Output, OutputKeyChanges)
}

func TestChanges_WarningDoesNotHideExpectedFailure(t *testing.T) {
	t.Parallel()

	cfg := nsConfig(map[string][]string{"eu-west": {"ns1.acme.com"}})
	cfg.ExpectedValues = []string{"ns1.acme.com"}
	cfg.OnChange = checkconfig.OnChangeWarning

	res, err := stubChecker([]string{"ns2.acme.com"}, nil).Execute(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, checkerdef.StatusDown, res.Status, "the worse status wins")
}

func TestChanges_RegionSelection(t *testing.T) {
	t.Parallel()

	baseline := map[string][]string{
		"eu-west": {"1.1.1.1"},
		"us-east": {"2.2.2.2"},
	}

	newCfg := func(region string) *DNSConfig {
		cfg := &DNSConfig{Host: "acme.com", RecordType: recordTypeA, DetectChanges: true, Baseline: baseline}
		cfg.SelectRegion(region)

		return cfg
	}

	t.Run("matches its own region", func(t *testing.T) {
		t.Parallel()

		res, err := stubChecker([]string{"2.2.2.2"}, nil).Execute(context.Background(), newCfg("us-east"))
		require.NoError(t, err)
		require.Equal(t, checkerdef.StatusUp, res.Status)
		require.NotContains(t, res.Output, OutputKeyChanges)
	})

	t.Run("differs from its own region", func(t *testing.T) {
		t.Parallel()

		res, err := stubChecker([]string{"2.2.2.2"}, nil).Execute(context.Background(), newCfg("eu-west"))
		require.NoError(t, err)
		require.Equal(t, checkerdef.StatusDown, res.Status)
	})

	t.Run("absent region captures", func(t *testing.T) {
		t.Parallel()

		res, err := stubChecker([]string{"3.3.3.3"}, nil).Execute(context.Background(), newCfg("ap-south"))
		require.NoError(t, err)
		require.Equal(t, checkerdef.StatusUp, res.Status)
		require.Equal(t, []string{"3.3.3.3"}, res.Output[OutputKeyBaselineCapture])
	})
}

func TestChanges_DisabledByDefault(t *testing.T) {
	t.Parallel()

	cfg := &DNSConfig{Host: "acme.com", RecordType: recordTypeNS}

	res, err := stubChecker([]string{"ns1.acme.com"}, nil).Execute(context.Background(), cfg)
	require.NoError(t, err)
	require.NotContains(t, res.Output, OutputKeyBaselineCapture)
	require.NotContains(t, res.Metrics, MetricChanged)
}
