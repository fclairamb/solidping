package checkdomain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

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
		{
			name: "valid explicit method",
			spec: &checkerdef.CheckSpec{
				Config: map[string]any{
					"domain": "google.com",
					"method": "rdap",
				},
			},
			wantErr: false,
		},
		{
			name: "invalid method",
			spec: &checkerdef.CheckSpec{
				Config: map[string]any{
					"domain": "google.com",
					"method": "carrier-pigeon",
				},
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

// ── RDAP/WHOIS dispatch tests ──
//
// These use an httptest fake serving both an IANA-shaped bootstrap document
// and RDAP domain responses, wired in via DomainChecker's unexported
// rdapClient test seam. No real network access.

// fakeRDAPServer serves a bootstrap document advertising rdapTLDs and routes
// GET /rdap/domain/{name} to domainHandler. bootstrapHits/domainHits count
// calls, so tests can assert a path was (not) taken.
type fakeRDAPServer struct {
	*httptest.Server

	bootstrapHits atomic.Int32
	domainHits    atomic.Int32
}

func newFakeRDAPServer(t *testing.T, rdapTLDs []string, domainHandler http.HandlerFunc) *fakeRDAPServer {
	t.Helper()

	fs := &fakeRDAPServer{}

	mux := http.NewServeMux()
	mux.HandleFunc("/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		fs.bootstrapHits.Add(1)

		base := "http://" + r.Host + "/rdap/"
		doc := map[string]any{
			"services": []any{
				[]any{rdapTLDs, []any{base}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(doc))
	})
	mux.HandleFunc("/rdap/domain/", func(w http.ResponseWriter, r *http.Request) {
		fs.domainHits.Add(1)
		domainHandler(w, r)
	})

	fs.Server = httptest.NewServer(mux)
	t.Cleanup(fs.Server.Close)

	return fs
}

// client returns an rdapClient wired to this fake server, for injection into
// DomainChecker.rdapClient.
func (fs *fakeRDAPServer) client() *rdapClient {
	return &rdapClient{
		httpClient:   fs.Server.Client(),
		bootstrapURL: fs.Server.URL + "/bootstrap",
		cacheTTL:     time.Hour,
	}
}

// rdapSuccessHandler answers every domain query with an expiration event and
// a registrar entity.
func rdapSuccessHandler(expiry time.Time, registrar string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{
			"events": []any{
				map[string]any{"eventAction": "expiration", "eventDate": expiry.Format(time.RFC3339)},
			},
			"entities": []any{
				map[string]any{
					"roles": []string{"registrar"},
					"vcardArray": []any{
						"vcard",
						[]any{
							[]any{"version", map[string]any{}, "text", "4.0"},
							[]any{"fn", map[string]any{}, "text", registrar},
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/rdap+json")
		_ = json.NewEncoder(w).Encode(resp) //nolint:errcheck // test helper
	}
}

// rdapMissingExpirationHandler answers with an events array that has no
// "expiration" action.
func rdapMissingExpirationHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{
			"events": []any{
				map[string]any{"eventAction": "registration", "eventDate": "2015-01-01T00:00:00Z"},
			},
		}
		w.Header().Set("Content-Type", "application/rdap+json")
		_ = json.NewEncoder(w).Encode(resp) //nolint:errcheck // test helper
	}
}

// rdapErrorHandler answers every domain query with a 500.
func rdapErrorHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// whoisFixture builds raw WHOIS text (generic gTLD format) that
// likexian/whois-parser can parse, with a fixed registrar/expiration.
func whoisFixture(registrar, expiryRFC3339 string) string {
	return "Domain Name: EXAMPLE-WHOIS-TEST.COM\n" +
		"Registry Domain ID: 123456_DOMAIN_COM-VRSN\n" +
		"Registrar WHOIS Server: whois.example-registrar.test\n" +
		"Registrar URL: http://www.example-registrar.test\n" +
		"Updated Date: 2025-01-01T00:00:00.0Z\n" +
		"Creation Date: 2015-01-01T00:00:00.0Z\n" +
		"Registrar Registration Expiration Date: " + expiryRFC3339 + "\n" +
		"Registrar: " + registrar + "\n" +
		"Registrar IANA ID: 9999\n" +
		"Registrar Abuse Contact Email: abuse@example-registrar.test\n" +
		"Registrar Abuse Contact Phone: +1.5555555555\n" +
		"Domain Status: clientTransferProhibited\n" +
		"Name Server: ns1.example-registrar.test\n" +
		"Name Server: ns2.example-registrar.test\n" +
		"DNSSEC: unsigned\n" +
		">>> Last update of WHOIS database: 2025-01-01T00:00:00.0Z <<<\n"
}

// countingWhoisFunc returns a whoisFunc stub and a call counter, so tests can
// assert WHOIS was (not) invoked.
func countingWhoisFunc(raw string, err error) (func(string) (string, error), *atomic.Int32) {
	var calls atomic.Int32

	return func(string) (string, error) {
		calls.Add(1)

		return raw, err
	}, &calls
}

func TestDomainChecker_Execute_RDAPSuccess(t *testing.T) {
	t.Parallel()

	expiry := time.Now().Add(365 * 24 * time.Hour) //nolint:mnd // ~1 year out, well past any threshold
	srv := newFakeRDAPServer(t, []string{"com"}, rdapSuccessHandler(expiry, "RDAP Registrar Inc."))
	whoisFunc, whoisCalls := countingWhoisFunc("", nil)

	for _, method := range []string{"", "auto", "rdap"} {
		t.Run("method="+method, func(t *testing.T) {
			t.Parallel()

			checker := &DomainChecker{rdapClient: srv.client(), whoisFunc: whoisFunc}
			cfg := &DomainConfig{Domain: "example.com", ThresholdDays: 30, Method: method}

			result, err := checker.Execute(context.Background(), cfg)
			require.NoError(t, err)
			require.Equal(t, checkerdef.StatusUp, result.Status)
			require.Equal(t, "rdap", result.Output["method"])
			require.Equal(t, "RDAP Registrar Inc.", result.Output["registrar"])
			require.Equal(t, int32(0), whoisCalls.Load(), "WHOIS must not be called when RDAP succeeds (positive control)")
		})
	}
}

func TestDomainChecker_Execute_RDAPFailureFallsBackToWHOIS(t *testing.T) {
	t.Parallel()

	srv := newFakeRDAPServer(t, []string{"com"}, rdapErrorHandler())
	whoisFunc, whoisCalls := countingWhoisFunc(whoisFixture("WHOIS Registrar LLC", "2099-01-01T00:00:00.0Z"), nil)

	checker := &DomainChecker{rdapClient: srv.client(), whoisFunc: whoisFunc}
	cfg := &DomainConfig{Domain: "example.com", ThresholdDays: 30, Method: "auto"}

	result, err := checker.Execute(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, checkerdef.StatusUp, result.Status)
	require.Equal(t, "whois", result.Output["method"])
	require.Equal(t, "WHOIS Registrar LLC", result.Output["registrar"])
	require.Equal(t, int32(1), whoisCalls.Load())
}

func TestDomainChecker_Execute_UnknownTLD(t *testing.T) {
	t.Parallel()

	// Bootstrap only knows "com"; the domain under test is ".zzz".
	srv := newFakeRDAPServer(t, []string{"com"}, rdapSuccessHandler(time.Now().Add(24*time.Hour), "irrelevant")) //nolint:mnd

	t.Run("auto falls back to whois", func(t *testing.T) {
		t.Parallel()

		whoisFunc, whoisCalls := countingWhoisFunc(whoisFixture("WHOIS Registrar LLC", "2099-01-01T00:00:00.0Z"), nil)
		checker := &DomainChecker{rdapClient: srv.client(), whoisFunc: whoisFunc}
		cfg := &DomainConfig{Domain: "example.zzz", ThresholdDays: 30, Method: "auto"}

		result, err := checker.Execute(context.Background(), cfg)
		require.NoError(t, err)
		require.Equal(t, "whois", result.Output["method"])
		require.Equal(t, int32(1), whoisCalls.Load())
	})

	t.Run("rdap does not fall back", func(t *testing.T) {
		t.Parallel()

		whoisFunc, whoisCalls := countingWhoisFunc("", nil)
		checker := &DomainChecker{rdapClient: srv.client(), whoisFunc: whoisFunc}
		cfg := &DomainConfig{Domain: "example.zzz", ThresholdDays: 30, Method: "rdap"}

		result, err := checker.Execute(context.Background(), cfg)
		require.NoError(t, err)
		require.Equal(t, checkerdef.StatusDown, result.Status)
		require.Contains(t, result.Output["error"], "RDAP lookup failed")
		require.Equal(t, int32(0), whoisCalls.Load())
	})
}

func TestDomainChecker_Execute_MissingExpirationEvent(t *testing.T) {
	t.Parallel()

	srv := newFakeRDAPServer(t, []string{"com"}, rdapMissingExpirationHandler())

	t.Run("auto falls back to whois", func(t *testing.T) {
		t.Parallel()

		whoisFunc, whoisCalls := countingWhoisFunc(whoisFixture("WHOIS Registrar LLC", "2099-01-01T00:00:00.0Z"), nil)
		checker := &DomainChecker{rdapClient: srv.client(), whoisFunc: whoisFunc}
		cfg := &DomainConfig{Domain: "example.com", ThresholdDays: 30, Method: "auto"}

		result, err := checker.Execute(context.Background(), cfg)
		require.NoError(t, err)
		require.Equal(t, "whois", result.Output["method"])
		require.Equal(t, int32(1), whoisCalls.Load())
	})

	t.Run("rdap does not fall back", func(t *testing.T) {
		t.Parallel()

		whoisFunc, whoisCalls := countingWhoisFunc("", nil)
		checker := &DomainChecker{rdapClient: srv.client(), whoisFunc: whoisFunc}
		cfg := &DomainConfig{Domain: "example.com", ThresholdDays: 30, Method: "rdap"}

		result, err := checker.Execute(context.Background(), cfg)
		require.NoError(t, err)
		require.Equal(t, checkerdef.StatusDown, result.Status)
		require.Contains(t, result.Output["error"], "no expiration event")
		require.Equal(t, int32(0), whoisCalls.Load())
	})
}

func TestDomainChecker_Execute_MethodWhoisSkipsRDAPEntirely(t *testing.T) {
	t.Parallel()

	// Even though the fake server would happily answer, method=whois must
	// never contact it at all.
	srv := newFakeRDAPServer(t, []string{"com"}, rdapSuccessHandler(time.Now().Add(24*time.Hour), "irrelevant")) //nolint:mnd
	whoisFunc, whoisCalls := countingWhoisFunc(whoisFixture("WHOIS Registrar LLC", "2099-01-01T00:00:00.0Z"), nil)

	checker := &DomainChecker{rdapClient: srv.client(), whoisFunc: whoisFunc}
	cfg := &DomainConfig{Domain: "example.com", ThresholdDays: 30, Method: "whois"}

	result, err := checker.Execute(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, "whois", result.Output["method"])
	require.Equal(t, int32(1), whoisCalls.Load())
	require.Equal(t, int32(0), srv.bootstrapHits.Load(), "method=whois must never contact RDAP")
	require.Equal(t, int32(0), srv.domainHits.Load(), "method=whois must never contact RDAP")
}

func TestDomainChecker_Execute_ContextCancellation(t *testing.T) {
	t.Parallel()

	srv := newFakeRDAPServer(t, []string{"com"}, rdapSuccessHandler(time.Now().Add(24*time.Hour), "irrelevant")) //nolint:mnd
	whoisFunc, whoisCalls := countingWhoisFunc("", nil)

	checker := &DomainChecker{rdapClient: srv.client(), whoisFunc: whoisFunc}
	cfg := &DomainConfig{Domain: "example.com", ThresholdDays: 30, Method: "rdap"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := checker.Execute(ctx, cfg)
	require.NoError(t, err)
	require.Equal(t, checkerdef.StatusDown, result.Status)
	require.Contains(t, result.Output["error"], "context canceled")
	require.Equal(t, int32(0), whoisCalls.Load(), "method=rdap must not fall back even on ctx cancellation")
}
