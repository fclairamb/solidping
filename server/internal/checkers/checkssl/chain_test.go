package checkssl

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// makeCert mints a self-contained certificate whose NotAfter is `days` from now.
// The certificate is not chained to a real CA — these tests drive buildResult
// directly with a fabricated ConnectionState, so trust is irrelevant.
func makeCert(t *testing.T, commonName string, days int) *x509.Certificate {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: commonName},
		Issuer:       pkix.Name{CommonName: "Test Issuer"},
		NotBefore:    time.Now().Add(-24 * time.Hour),
		// Add a 12h buffer so the truncating days-remaining computation
		// (time.Until/24h, floored) lands on exactly `days` despite the small
		// elapsed time between minting and evaluation.
		NotAfter: time.Now().Add(time.Duration(days)*24*time.Hour + 12*time.Hour),
		DNSNames: []string{commonName},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	return cert
}

// buildResultFromChain runs the production buildResult against a fabricated
// handshake state carrying the given chain.
func buildResultFromChain(
	chain []*x509.Certificate, verified bool, params execParams,
) *checkerdef.Result {
	state := tls.ConnectionState{
		Version:          tls.VersionTLS13,
		PeerCertificates: chain,
	}
	if verified {
		state.VerifiedChains = [][]*x509.Certificate{chain}
	}

	conn := &tlsConnResult{
		connectionTime: 5 * time.Millisecond,
		handshakeTime:  5 * time.Millisecond,
		state:          state,
	}

	checker := &SSLChecker{}

	return checker.buildResult(conn, "127.0.0.1", params, false, time.Now())
}

func TestBuildResult_ChainExpiryTiers(t *testing.T) {
	t.Parallel()

	// Tiers used for the explicit two-tier cases: warning 30, critical 7.
	twoTier := execParams{port: 443, warningDays: 30, criticalDays: 7, host: "example.com"}

	tests := []struct {
		name              string
		chain             []*x509.Certificate
		params            execParams
		wantStatus        checkerdef.Status
		wantSeverity      string
		wantWarningFlag   bool
		wantMinDays       int
		wantChainLength   int
		wantChainVerified bool
	}{
		{
			// Leaf far out (200d) but intermediate inside warning window (20d)
			// drives the minimum — proves whole-chain min, not leaf.
			name: "intermediate inside warning drives min → warning",
			chain: []*x509.Certificate{
				makeCert(t, "leaf.example.com", 200),
				makeCert(t, "Intermediate CA", 20),
			},
			params:            twoTier,
			wantStatus:        checkerdef.StatusWarning,
			wantSeverity:      checkerdef.SeverityWarning,
			wantWarningFlag:   true,
			wantMinDays:       20,
			wantChainLength:   2,
			wantChainVerified: true,
		},
		{
			name: "min inside critical → down/critical",
			chain: []*x509.Certificate{
				makeCert(t, "leaf.example.com", 200),
				makeCert(t, "Intermediate CA", 5),
			},
			params:          twoTier,
			wantStatus:      checkerdef.StatusDown,
			wantSeverity:    checkerdef.SeverityCritical,
			wantWarningFlag: false,
			wantMinDays:     5,
			wantChainLength: 2,
		},
		{
			name: "min between tiers → warning/warning",
			chain: []*x509.Certificate{
				makeCert(t, "leaf.example.com", 25),
			},
			params:          twoTier,
			wantStatus:      checkerdef.StatusWarning,
			wantSeverity:    checkerdef.SeverityWarning,
			wantWarningFlag: true,
			wantMinDays:     25,
			wantChainLength: 1,
		},
		{
			name: "min beyond both → up, no flag",
			chain: []*x509.Certificate{
				makeCert(t, "leaf.example.com", 200),
				makeCert(t, "Intermediate CA", 100),
			},
			params:          twoTier,
			wantStatus:      checkerdef.StatusUp,
			wantSeverity:    checkerdef.SeverityNone,
			wantWarningFlag: false,
			wantMinDays:     100,
			wantChainLength: 2,
		},
		{
			// Legacy thresholdDays only (30), resolved to critical=warning=30
			// via effectiveThresholds → behaves exactly as today: Down at 30.
			name: "legacy threshold behaves as today (down at 30)",
			chain: []*x509.Certificate{
				makeCert(t, "leaf.example.com", 20),
			},
			params: newExecParams(&SSLConfig{
				Host:          "example.com",
				ThresholdDays: 30,
			}),
			wantStatus:      checkerdef.StatusDown,
			wantSeverity:    checkerdef.SeverityCritical,
			wantWarningFlag: false,
			wantMinDays:     20,
			wantChainLength: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			res := buildResultFromChain(tt.chain, true, tt.params)

			r.Equal(tt.wantStatus, res.Status)
			r.Equal(tt.wantSeverity, res.Output["severity"])
			r.Equal(tt.wantMinDays, res.Metrics["days_remaining"])
			r.Equal(tt.wantChainLength, res.Output["chainLength"])

			if tt.wantWarningFlag {
				r.Equal(true, res.Output["certExpiryWarning"])
			} else {
				r.NotContains(res.Output, "certExpiryWarning")
			}

			chain, ok := res.Output["chain"].([]chainCertReport)
			r.True(ok, "chain output should be a []chainCertReport")
			r.Len(chain, tt.wantChainLength)

			soonest, ok := res.Output["soonestExpiring"].(map[string]any)
			r.True(ok, "soonestExpiring should be present")
			r.Equal(tt.wantMinDays, soonest["daysRemaining"])
		})
	}
}

func TestBuildResult_ChainVerifiedFlag(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	chain := []*x509.Certificate{makeCert(t, "leaf.example.com", 200)}
	params := execParams{port: 443, warningDays: 30, criticalDays: 7, host: "example.com"}

	verified := buildResultFromChain(chain, true, params)
	r.Equal(true, verified.Output["chainVerified"])

	unverified := buildResultFromChain(chain, false, params)
	r.Equal(false, unverified.Output["chainVerified"])
}

func TestSSLConfig_LegacyAlias(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &SSLConfig{}
	r.NoError(cfg.FromMap(map[string]any{
		"host":          "example.com",
		"thresholdDays": float64(14),
	}))
	r.NoError(cfg.Validate())

	// Legacy threshold maps onto the critical tier.
	r.Equal(14, cfg.CriticalDays)

	warning, critical := cfg.effectiveThresholds()
	r.Equal(14, critical)
	r.Equal(30, warning) // default warning, clamped up to >= critical
}

func TestSSLConfig_Validate_WarningBelowCritical(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &SSLConfig{
		Host:         "example.com",
		WarningDays:  7,
		CriticalDays: 30,
	}
	err := cfg.Validate()
	r.Error(err)
	r.Contains(err.Error(), "warningDays")
}

func TestSSLConfig_Validate_TwoTierValid(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &SSLConfig{
		Host:         "example.com",
		WarningDays:  30,
		CriticalDays: 7,
	}
	r.NoError(cfg.Validate())
}
