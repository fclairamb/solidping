package checkdnsbl

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// fakeLookuper is a map-backed hostLookuper for tests. The map is keyed by the
// queried host; a value of errNXDOMAIN means NXDOMAIN (clean), any other
// non-nil error means inconclusive, and a slice of addrs means listed/resolved.
type fakeLookuper struct {
	responses map[string]fakeResp
	fallback  fakeResp
}

type fakeResp struct {
	addrs []string
	err   error
}

var errNXDOMAIN = &net.DNSError{Err: "no such host", IsNotFound: true}

func (f *fakeLookuper) LookupHost(_ context.Context, host string) ([]string, error) {
	if r, ok := f.responses[host]; ok {
		return r.addrs, r.err
	}

	return f.fallback.addrs, f.fallback.err
}

func TestDNSBLChecker_Type(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	checker := &DNSBLChecker{}
	r.Equal(checkerdef.CheckTypeDNSBL, checker.Type())
}

func TestDNSBLConfig_FromMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configMap map[string]any
		wantErr   bool
		errMsg    string
		validate  func(*require.Assertions, *DNSBLConfig)
	}{
		{
			name: "valid config with all fields",
			configMap: map[string]any{
				"target":     sampleMailServerIP,
				"blocklists": []any{zoneSpamhaus, zoneSpamcop},
				"nameserver": "127.0.0.1:53",
				"timeout":    "15s",
			},
			validate: func(r *require.Assertions, cfg *DNSBLConfig) {
				r.Equal(sampleMailServerIP, cfg.Target)
				r.Equal([]string{zoneSpamhaus, zoneSpamcop}, cfg.Blocklists)
				r.Equal("127.0.0.1:53", cfg.Nameserver)
				r.Equal(15*time.Second, cfg.Timeout)
			},
		},
		{
			name:      "minimal config defaults applied at resolve",
			configMap: map[string]any{"target": "127.0.0.2"},
			validate: func(r *require.Assertions, cfg *DNSBLConfig) {
				r.Equal("127.0.0.2", cfg.Target)
				r.Empty(cfg.Blocklists)
				r.Equal(defaultBlocklists, cfg.resolveBlocklists())
			},
		},
		{
			name:      "blocklists as []string",
			configMap: map[string]any{"target": "1.2.3.4", "blocklists": []string{zoneSpamhaus}},
			validate: func(r *require.Assertions, cfg *DNSBLConfig) {
				r.Equal([]string{zoneSpamhaus}, cfg.Blocklists)
			},
		},
		{
			name:      "invalid target type",
			configMap: map[string]any{"target": 123},
			wantErr:   true,
			errMsg:    "target: must be a string",
		},
		{
			name:      "invalid nameserver type",
			configMap: map[string]any{"target": "1.2.3.4", "nameserver": 123},
			wantErr:   true,
			errMsg:    "nameserver: must be a string",
		},
		{
			name:      "invalid timeout type",
			configMap: map[string]any{"target": "1.2.3.4", "timeout": 123},
			wantErr:   true,
			errMsg:    "timeout: must be a string",
		},
		{
			name:      "invalid timeout value",
			configMap: map[string]any{"target": "1.2.3.4", "timeout": "not-a-duration"},
			wantErr:   true,
			errMsg:    "timeout: must be a valid duration string",
		},
		{
			name:      "invalid blocklists element type",
			configMap: map[string]any{"target": "1.2.3.4", "blocklists": []any{"ok", 5}},
			wantErr:   true,
			errMsg:    "blocklists: must be a string array",
		},
		{
			name:      "invalid blocklists type",
			configMap: map[string]any{"target": "1.2.3.4", "blocklists": zoneSpamhaus},
			wantErr:   true,
			errMsg:    "blocklists: must be a string array",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			cfg := &DNSBLConfig{}
			err := cfg.FromMap(tt.configMap)

			if tt.wantErr {
				r.Error(err)
				r.Equal(tt.errMsg, err.Error())

				return
			}

			r.NoError(err)

			if tt.validate != nil {
				tt.validate(r, cfg)
			}
		})
	}
}

func TestDNSBLConfig_GetConfig(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &DNSBLConfig{
		Target:     sampleMailServerIP,
		Blocklists: []string{zoneSpamhaus},
		Nameserver: "127.0.0.1:53",
		Timeout:    15 * time.Second,
	}

	got := cfg.GetConfig()
	r.Equal(sampleMailServerIP, got["target"])
	r.Equal([]string{zoneSpamhaus}, got["blocklists"])
	r.Equal("127.0.0.1:53", got["nameserver"])
	r.Equal("15s", got["timeout"])

	// Round-trip through FromMap should preserve everything.
	rt := &DNSBLConfig{}
	r.NoError(rt.FromMap(map[string]any{
		"target":     got["target"],
		"blocklists": got["blocklists"],
		"nameserver": got["nameserver"],
		"timeout":    got["timeout"],
	}))
	r.Equal(cfg, rt)
}

func TestDNSBLChecker_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   *DNSBLConfig
		wantErr  bool
		errMsg   string
		wantSlug string
	}{
		{
			name:     "valid minimal config",
			config:   &DNSBLConfig{Target: sampleMailServerIP},
			wantSlug: "dnsbl-203-0-113-10",
		},
		{
			name:     "valid full config",
			config:   &DNSBLConfig{Target: "mail.example.com", Nameserver: "127.0.0.1:53", Timeout: 20 * time.Second},
			wantSlug: "dnsbl-mail-example-com",
		},
		{
			name:    "empty target",
			config:  &DNSBLConfig{},
			wantErr: true,
			errMsg:  "target: is required",
		},
		{
			name:    "bad nameserver",
			config:  &DNSBLConfig{Target: "1.2.3.4", Nameserver: "127.0.0.1"},
			wantErr: true,
			errMsg:  "nameserver: must be in format host:port, got 127.0.0.1",
		},
		{
			name:    "timeout too long",
			config:  &DNSBLConfig{Target: "1.2.3.4", Timeout: 61 * time.Second},
			wantErr: true,
			errMsg:  "timeout: must be > 0 and <= 30s, got 1m1s",
		},
		{
			name:    "negative timeout",
			config:  &DNSBLConfig{Target: "1.2.3.4", Timeout: -1 * time.Second},
			wantErr: true,
			errMsg:  "timeout: must be > 0 and <= 30s, got -1s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			checker := &DNSBLChecker{}
			spec := &checkerdef.CheckSpec{Config: tt.config.GetConfig()}
			err := checker.Validate(spec)

			if tt.wantErr {
				r.Error(err)
				r.Equal(tt.errMsg, err.Error())

				return
			}

			r.NoError(err)
			r.NotEqual("x", spec.Slug, "auto-generated slug must never fall back to the bare sanitize-padding value")
			r.Equal(tt.wantSlug, spec.Slug)
		})
	}
}

func TestReverseIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "simple", in: "1.2.3.4", want: "4.3.2.1"},
		{name: "loopback test entry", in: "127.0.0.2", want: "2.0.0.127"},
		{name: "ipv6 rejected", in: "2001:db8::1", want: ""},
		{name: "not an ip", in: "example.com", want: ""},
		{name: "empty", in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			r.Equal(tt.want, reverseIP(tt.in))
		})
	}
}

func TestDNSBLChecker_Execute(t *testing.T) {
	t.Parallel()

	const ipMixed = "198.51.100.5"

	ipListed := sampleMailServerIP

	listedQuery := reverseIP(ipListed) + "." + zoneSpamhaus      // listed on spamhaus
	cleanQuery := reverseIP(ipListed) + "." + zoneSpamcop        // clean on spamcop
	inconclQuery := reverseIP(ipMixed) + "." + zoneSpamcop       // errors out
	cleanInconclQuery := reverseIP(ipMixed) + "." + zoneSpamhaus // clean

	tests := []struct {
		name             string
		config           *DNSBLConfig
		lookuper         hostLookuper
		wantStatus       checkerdef.Status
		wantListedOn     []string
		wantClean        []string
		wantInconclusive []string
		wantListings     int
		wantReturnCodes  map[string][]string
		wantErrorCodes   map[string][]string
	}{
		{
			name:   "listed on one zone",
			config: &DNSBLConfig{Target: sampleMailServerIP, Blocklists: []string{zoneSpamhaus, zoneSpamcop}},
			lookuper: &fakeLookuper{
				responses: map[string]fakeResp{
					listedQuery: {addrs: []string{"127.0.0.2"}},
					cleanQuery:  {err: errNXDOMAIN},
				},
			},
			wantStatus:   checkerdef.StatusDown,
			wantListedOn: []string{zoneSpamhaus},
			wantClean:    []string{zoneSpamcop},
			wantListings: 1,
		},
		{
			name:   "clean on all zones",
			config: &DNSBLConfig{Target: sampleMailServerIP, Blocklists: []string{zoneSpamhaus, zoneSpamcop}},
			lookuper: &fakeLookuper{
				fallback: fakeResp{err: errNXDOMAIN},
			},
			wantStatus:   checkerdef.StatusUp,
			wantClean:    []string{zoneSpamcop, zoneSpamhaus},
			wantListings: 0,
		},
		{
			name:   "mixed clean and inconclusive is up",
			config: &DNSBLConfig{Target: "198.51.100.5", Blocklists: []string{zoneSpamhaus, zoneSpamcop}},
			lookuper: &fakeLookuper{
				responses: map[string]fakeResp{
					cleanInconclQuery: {err: errNXDOMAIN},
					inconclQuery:      {err: &net.DNSError{Err: "server misbehaving", IsTemporary: true}},
				},
			},
			wantStatus:       checkerdef.StatusUp,
			wantClean:        []string{zoneSpamhaus},
			wantInconclusive: []string{zoneSpamcop},
			wantListings:     0,
		},
		{
			name:   "all zones inconclusive is timeout",
			config: &DNSBLConfig{Target: sampleMailServerIP, Blocklists: []string{zoneSpamhaus, zoneSpamcop}},
			lookuper: &fakeLookuper{
				fallback: fakeResp{err: &net.DNSError{Err: "timeout", IsTimeout: true}},
			},
			wantStatus:       checkerdef.StatusTimeout,
			wantInconclusive: []string{zoneSpamcop, zoneSpamhaus},
			wantListings:     0,
		},
		{
			// Spamhaus refuses queries from public cloud resolvers with
			// 127.255.255.254 — an error reply, not a listing. The clean zone
			// keeps the check Up.
			name:   "error code only zone is inconclusive not listed",
			config: &DNSBLConfig{Target: sampleMailServerIP, Blocklists: []string{zoneSpamhaus, zoneSpamcop}},
			lookuper: &fakeLookuper{
				responses: map[string]fakeResp{
					listedQuery: {addrs: []string{"127.255.255.254"}},
					cleanQuery:  {err: errNXDOMAIN},
				},
			},
			wantStatus:       checkerdef.StatusUp,
			wantClean:        []string{zoneSpamcop},
			wantInconclusive: []string{zoneSpamhaus},
			wantListings:     0,
			wantErrorCodes:   map[string][]string{zoneSpamhaus: {"127.255.255.254"}},
		},
		{
			name:   "multiple error codes only is inconclusive",
			config: &DNSBLConfig{Target: sampleMailServerIP, Blocklists: []string{zoneSpamhaus, zoneSpamcop}},
			lookuper: &fakeLookuper{
				responses: map[string]fakeResp{
					listedQuery: {addrs: []string{"127.255.255.255", "127.255.255.252"}},
					cleanQuery:  {err: errNXDOMAIN},
				},
			},
			wantStatus:       checkerdef.StatusUp,
			wantClean:        []string{zoneSpamcop},
			wantInconclusive: []string{zoneSpamhaus},
			wantListings:     0,
			wantErrorCodes:   map[string][]string{zoneSpamhaus: {"127.255.255.255", "127.255.255.252"}},
		},
		{
			// A genuine 127.0.0.x listing must still drive Down (no regression).
			name:   "real listing is still down",
			config: &DNSBLConfig{Target: sampleMailServerIP, Blocklists: []string{zoneSpamhaus, zoneSpamcop}},
			lookuper: &fakeLookuper{
				responses: map[string]fakeResp{
					listedQuery: {addrs: []string{"127.0.0.2"}},
					cleanQuery:  {err: errNXDOMAIN},
				},
			},
			wantStatus:      checkerdef.StatusDown,
			wantListedOn:    []string{zoneSpamhaus},
			wantClean:       []string{zoneSpamcop},
			wantListings:    1,
			wantReturnCodes: map[string][]string{zoneSpamhaus: {"127.0.0.2"}},
		},
		{
			// Mixed answer: keep the genuine listing, drop the error code from
			// return_codes but surface it under error_codes.
			name:   "mixed real and error code lists on real only",
			config: &DNSBLConfig{Target: sampleMailServerIP, Blocklists: []string{zoneSpamhaus, zoneSpamcop}},
			lookuper: &fakeLookuper{
				responses: map[string]fakeResp{
					listedQuery: {addrs: []string{"127.0.0.4", "127.255.255.254"}},
					cleanQuery:  {err: errNXDOMAIN},
				},
			},
			wantStatus:      checkerdef.StatusDown,
			wantListedOn:    []string{zoneSpamhaus},
			wantClean:       []string{zoneSpamcop},
			wantListings:    1,
			wantReturnCodes: map[string][]string{zoneSpamhaus: {"127.0.0.4"}},
			wantErrorCodes:  map[string][]string{zoneSpamhaus: {"127.255.255.254"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			checker := &DNSBLChecker{lookuper: tt.lookuper}

			result, err := checker.Execute(context.Background(), tt.config)
			r.NoError(err)
			r.NotNil(result)
			r.Equal(tt.wantStatus, result.Status)

			r.Equal(tt.wantListings, result.Metrics["listings"])
			r.Equal(len(tt.config.Blocklists), result.Metrics["blocklists_checked"])

			if tt.wantListedOn != nil {
				r.Equal(tt.wantListedOn, result.Output["listed_on"])
			}

			if tt.wantClean != nil {
				r.Equal(tt.wantClean, result.Output["clean"])
			}

			if tt.wantInconclusive != nil {
				r.Equal(tt.wantInconclusive, result.Output["inconclusive"])
			}

			if tt.wantReturnCodes != nil {
				r.Equal(tt.wantReturnCodes, result.Output["return_codes"])
			}

			if tt.wantErrorCodes != nil {
				r.Equal(tt.wantErrorCodes, result.Output["error_codes"])
			} else {
				r.NotContains(result.Output, "error_codes")
			}
		})
	}
}

func TestIsDNSBLErrorCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "refused via public resolver", in: "127.255.255.254", want: true},
		{name: "rate limited", in: "127.255.255.255", want: true},
		{name: "malformed zone", in: "127.255.255.252", want: true},
		{name: "block start", in: "127.255.255.0", want: true},
		{name: "real SBL listing", in: "127.0.0.2", want: false},
		{name: "real XBL listing", in: "127.0.0.4", want: false},
		{name: "adjacent non-error range", in: "127.255.254.255", want: false},
		{name: "not an ip", in: "example.com", want: false},
		{name: "empty", in: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			r.Equal(tt.want, isDNSBLErrorCode(tt.in))
		})
	}
}

func TestDNSBLChecker_Execute_MultiIPHostname(t *testing.T) {
	t.Parallel()

	const zone = zoneSpamhaus

	r := require.New(t)

	ip1 := sampleMailServerIP
	ip2 := "203.0.113.11"

	lookuper := &fakeLookuper{
		responses: map[string]fakeResp{
			"mail.example.com": {addrs: []string{ip1, ip2}},
			// ip1 is listed, ip2 is clean — any listing makes the whole check Down.
			reverseIP(ip1) + "." + zone: {addrs: []string{"127.0.0.2"}},
			reverseIP(ip2) + "." + zone: {err: errNXDOMAIN},
		},
	}

	checker := &DNSBLChecker{lookuper: lookuper}
	result, err := checker.Execute(context.Background(), &DNSBLConfig{
		Target:     "mail.example.com",
		Blocklists: []string{zone},
	})
	r.NoError(err)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal([]string{zone}, result.Output["listed_on"])
	r.Equal(ip1+","+ip2, result.Output["target_ip"])
}

func TestDNSBLChecker_Execute_UnresolvableTarget(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	lookuper := &fakeLookuper{
		responses: map[string]fakeResp{
			"nope.invalid": {err: errNXDOMAIN},
		},
	}

	checker := &DNSBLChecker{lookuper: lookuper}
	result, err := checker.Execute(context.Background(), &DNSBLConfig{Target: "nope.invalid"})
	r.NoError(err)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Contains(result.Output[checkerdef.OutputKeyError], "failed to resolve target")
}

func TestDNSBLChecker_GetSampleConfigs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	checker := &DNSBLChecker{}
	samples := checker.GetSampleConfigs(nil)
	r.NotEmpty(samples)

	for _, s := range samples {
		r.NotEmpty(s.Config["target"])
	}
}
