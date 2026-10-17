package checkdns

import (
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// caaServer answers CAA queries for example.test with one issue record and
// NXDOMAIN for everything else, on a loopback UDP port.
func caaServer(t *testing.T) string {
	t.Helper()

	conn, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	require.NoError(t, err)

	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, req *dns.Msg) {
		resp := new(dns.Msg)
		resp.SetReply(req)

		if req.Question[0].Name == "example.test." && req.Question[0].Qtype == dns.TypeCAA {
			resp.Answer = append(resp.Answer, &dns.CAA{
				Hdr:   dns.RR_Header{Name: "example.test.", Rrtype: dns.TypeCAA, Class: dns.ClassINET, Ttl: 60},
				Flag:  0,
				Tag:   "issue",
				Value: "letsencrypt.org",
			})
		} else {
			resp.Rcode = dns.RcodeNameError
		}

		_ = w.WriteMsg(resp)
	})

	server := &dns.Server{PacketConn: conn, Handler: mux}

	go func() { _ = server.ActivateAndServe() }()

	t.Cleanup(func() { _ = server.Shutdown() })

	return conn.LocalAddr().String()
}

func TestDNSCheckerResolvesCAARecords(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	nameserver := caaServer(t)

	cfg := &DNSConfig{Host: "example.test", RecordType: "CAA", Nameserver: nameserver, Timeout: 2 * time.Second}
	r.NoError((&DNSChecker{}).Validate(&checkerdef.CheckSpec{Config: cfg.GetConfig()}))

	result, err := (&DNSChecker{}).Execute(t.Context(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	r.Equal([]string{`0 issue "letsencrypt.org"`}, result.Output["resolved_values"])
}

func TestDNSCheckerCAAOnAMissingNameIsDown(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	nameserver := caaServer(t)

	cfg := &DNSConfig{Host: "missing.test", RecordType: "CAA", Nameserver: nameserver, Timeout: 2 * time.Second}

	result, err := (&DNSChecker{}).Execute(t.Context(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusDown, result.Status, result.Output)
}
