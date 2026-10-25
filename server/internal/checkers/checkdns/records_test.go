package checkdns

import (
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// recordsServer answers A, AAAA, CNAME, MX, NS and TXT queries for acme.test.
func recordsServer(t *testing.T) string {
	t.Helper()

	conn, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	require.NoError(t, err)

	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, req *dns.Msg) {
		resp := new(dns.Msg)
		resp.SetReply(req)

		hdr := func(rt uint16) dns.RR_Header {
			return dns.RR_Header{Name: req.Question[0].Name, Rrtype: rt, Class: dns.ClassINET, Ttl: 60}
		}

		if req.Question[0].Name == "alias.test." {
			resp.Answer = append(resp.Answer, &dns.CNAME{Hdr: hdr(dns.TypeCNAME), Target: "target.acme.test."})
			_ = w.WriteMsg(resp)

			return
		}

		if req.Question[0].Name != "acme.test." {
			resp.Rcode = dns.RcodeNameError
			_ = w.WriteMsg(resp)

			return
		}

		switch req.Question[0].Qtype {
		case dns.TypeA:
			resp.Answer = append(resp.Answer, &dns.A{Hdr: hdr(dns.TypeA), A: net.ParseIP("192.0.2.7")})
		case dns.TypeAAAA:
			resp.Answer = append(resp.Answer, &dns.AAAA{Hdr: hdr(dns.TypeAAAA), AAAA: net.ParseIP("2001:db8::7")})
		case dns.TypeMX:
			resp.Answer = append(resp.Answer, &dns.MX{Hdr: hdr(dns.TypeMX), Preference: 10, Mx: "mx.acme.test."})
		case dns.TypeNS:
			resp.Answer = append(resp.Answer, &dns.NS{Hdr: hdr(dns.TypeNS), Ns: "ns1.acme.test."})
		case dns.TypeTXT:
			resp.Answer = append(resp.Answer, &dns.TXT{Hdr: hdr(dns.TypeTXT), Txt: []string{"v=acme1"}})
		}

		_ = w.WriteMsg(resp)
	})

	server := &dns.Server{PacketConn: conn, Handler: mux}

	go func() { _ = server.ActivateAndServe() }()

	t.Cleanup(func() { _ = server.Shutdown() })

	return conn.LocalAddr().String()
}

func TestDNSCheckerResolvesRecordTypes(t *testing.T) {
	t.Parallel()

	nameserver := recordsServer(t)

	tests := []struct {
		recordType string
		host       string
		key        string
		want       []string
	}{
		{"A", "acme.test", "resolved_ips", []string{"192.0.2.7"}},
		{"AAAA", "acme.test", "resolved_ips", []string{"2001:db8::7"}},
		{"CNAME", "alias.test", "resolved_values", []string{"target.acme.test"}},
		{"MX", "acme.test", "resolved_values", []string{"10 mx.acme.test"}},
		{"NS", "acme.test", "resolved_values", []string{"ns1.acme.test"}},
		{"TXT", "acme.test", "resolved_values", []string{"v=acme1"}},
	}

	for _, tt := range tests {
		t.Run(tt.recordType, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			cfg := &DNSConfig{Host: tt.host, RecordType: tt.recordType, Nameserver: nameserver, Timeout: 2 * time.Second}

			result, err := (&DNSChecker{}).Execute(t.Context(), cfg)
			r.NoError(err)
			r.Equal(checkerdef.StatusUp, result.Status, result.Output)
			r.Equal(tt.want, result.Output[tt.key])
			r.Equal(nameserver, result.Output["nameserver"])
		})
	}
}

func TestDNSCheckerExpectedIPsMismatchIsDown(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &DNSConfig{
		Host: "acme.test", RecordType: "A", Nameserver: recordsServer(t), Timeout: 2 * time.Second,
		ExpectedIPs: []string{"198.51.100.1"},
	}

	result, err := (&DNSChecker{}).Execute(t.Context(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal("resolved values do not match expected values", result.Output[checkerdef.OutputKeyError])

	cfg.ExpectedIPs = []string{"192.0.2.7"}
	result, err = (&DNSChecker{}).Execute(t.Context(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status)
}

func TestDNSCheckerLookupErrorsAreReturned(t *testing.T) {
	t.Parallel()

	nameserver := recordsServer(t)

	for _, rt := range []string{"A", "AAAA", "CNAME", "MX", "NS", "TXT"} {
		t.Run(rt, func(t *testing.T) {
			t.Parallel()

			cfg := &DNSConfig{Host: "nope.test", RecordType: rt, Nameserver: nameserver, Timeout: 2 * time.Second}

			result, err := (&DNSChecker{}).Execute(t.Context(), cfg)
			if err == nil {
				// NXDOMAIN may be reported as a down result rather than an error.
				require.Equal(t, checkerdef.StatusDown, result.Status)
			}
		})
	}
}

func TestDNSCheckerSOAIsNotSupported(t *testing.T) {
	t.Parallel()

	_, _, err := (&DNSChecker{}).resolve(
		t.Context(), &DNSConfig{Host: "acme.test", RecordType: "SOA", Nameserver: "127.0.0.1:1"}, "SOA", time.Second)
	require.ErrorIs(t, err, errSOANotSupported)
}

func TestLookupCAAWithoutNameserverFailsOnUnreachableResolver(t *testing.T) {
	t.Parallel()

	_, err := lookupCAA(t.Context(), "127.0.0.1:1", "acme.test", 200*time.Millisecond)
	require.Error(t, err)
}
