package checkdns

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSamples_RoundTrip verifies every DNS sample validates and survives a
// FromMap -> GetConfig -> FromMap round-trip (host, record type, nameserver,
// timeout preserved). This guards against samples that emit keys the config
// never reads, and against an invalid custom nameserver.
func TestSamples_RoundTrip(t *testing.T) {
	t.Parallel()

	checker := &DNSChecker{}

	for _, spec := range checker.GetSampleConfigs(nil) {
		t.Run(spec.Slug, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			cfg := &DNSConfig{}
			r.NoError(cfg.FromMap(spec.Config))
			r.NotEmpty(cfg.Host, "sample must set a host to query")
			r.NoError(checker.Validate(&spec))

			again := &DNSConfig{}
			r.NoError(again.FromMap(cfg.GetConfig()))
			r.Equal(cfg.Host, again.Host)
			r.Equal(cfg.RecordType, again.RecordType)
			r.Equal(cfg.Nameserver, again.Nameserver)
			r.Equal(cfg.Timeout, again.Timeout)
		})
	}
}

// TestSamples_IncludeCustomNameserver ensures at least one sample exercises the
// optional custom-resolver path with a valid host:port nameserver.
func TestSamples_IncludeCustomNameserver(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	checker := &DNSChecker{}

	var withNameserver int

	for _, spec := range checker.GetSampleConfigs(nil) {
		cfg := &DNSConfig{}
		r.NoError(cfg.FromMap(spec.Config))

		if cfg.Nameserver != "" {
			withNameserver++

			r.Contains(cfg.Nameserver, ":", "nameserver sample must be host:port")
		}
	}

	r.GreaterOrEqual(withNameserver, 1, "at least one DNS sample should set a custom nameserver")
}
