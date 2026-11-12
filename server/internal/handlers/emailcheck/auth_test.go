package emailcheck

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"blitiri.com.ar/go/spf"
	"github.com/emersion/go-msgauth/dkim"
	"github.com/stretchr/testify/require"

	emailconfig "github.com/fclairamb/solidping/server/internal/checkers/checkemail/config"
	"github.com/fclairamb/solidping/server/internal/jmap"
)

// stubResolver is a map-backed DNS: unknown names are NXDOMAIN. When block is
// set every lookup waits for the context instead, to simulate a dead resolver.
type stubResolver struct {
	mu    sync.Mutex
	txt   map[string][]string
	hosts map[string][]string
	block bool
}

func notFound(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (s *stubResolver) wait(ctx context.Context) error {
	if s.block {
		<-ctx.Done()

		return ctx.Err()
	}

	return nil
}

func (s *stubResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	if err := s.wait(ctx); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if v, ok := s.txt[strings.ToLower(name)]; ok {
		return v, nil
	}

	return nil, notFound(name)
}

func (s *stubResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	if err := s.wait(ctx); err != nil {
		return nil, err
	}

	if v, ok := s.hosts[host]; ok {
		return v, nil
	}

	return nil, notFound(host)
}

func (s *stubResolver) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	return nil, notFound(name)
}

func (s *stubResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	return nil, notFound(host)
}

func (s *stubResolver) LookupAddr(_ context.Context, addr string) ([]string, error) {
	return nil, notFound(addr)
}

var _ spf.DNSResolver = (*stubResolver)(nil)

func hdr(name, value string) jmap.EmailHeader { return jmap.EmailHeader{Name: name, Value: value} }

func TestParseReceivedAndTrustedHop(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	headers := []jmap.EmailHeader{
		hdr("Received", "from relay.internal (relay.internal [10.0.0.4]) by mx.provider.test with ESMTP"),
		hdr("Received", "from mail.example.com (mail.example.com [203.0.113.7]) by relay.internal with ESMTP"),
		// Forged by the sender: below the trusted hop, never used for the verdict.
		hdr("Received", "from forged.example (forged [198.51.100.99]) by mail.example.com"),
		hdr("Subject", "x"),
	}

	hops := parseReceived(headers)
	r.Len(hops, 3)

	hop := trustedHop(hops)
	r.Equal("203.0.113.7", hop.ip)
	r.Equal("mail.example.com", hop.helo)
}

func TestParseReceivedIPv6Literal(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	hops := parseReceived([]jmap.EmailHeader{
		hdr("Received", "from mail.example.com ([IPv6:2001:db8::25]) by mx.provider.test"),
	})
	r.Len(hops, 1)
	r.Equal("2001:db8::25", trustedHop(hops).ip)
}

func TestTrustedHopNoneWhenAllPrivate(t *testing.T) {
	t.Parallel()

	hops := parseReceived([]jmap.EmailHeader{
		hdr("Received", "from a (a [192.168.1.2]) by b"),
		hdr("Received", "from c (c [127.0.0.1]) by d"),
	})
	require.Empty(t, trustedHop(hops).ip)
}

func TestParseProviderAuth(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	got := parseProviderAuth([]jmap.EmailHeader{
		hdr("Authentication-Results", "mx.provider.test; spf=pass smtp.mailfrom=a@b; dkim=fail; dmarc=pass header.from=b"),
		hdr("Authentication-Results", "other; spf=fail"),
	})
	r.Equal(map[string]string{"spf": "pass", "dkim": "fail", "dmarc": "pass"}, got)
	r.Nil(parseProviderAuth(nil))
}

// signedMessage builds a message DKIM-signed by example.com with a fresh key and
// returns it with the DNS record that publishes the key.
func signedMessage(t *testing.T, body string) ([]byte, string) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	msg := "From: Alice <alice@example.com>\r\nTo: x@inbox.test\r\nSubject: probe\r\n" +
		"Date: Thu, 12 Nov 2026 10:00:00 +0000\r\nMessage-ID: <1@example.com>\r\n\r\n" + body + "\r\n"

	var out bytes.Buffer

	require.NoError(t, dkim.Sign(&out, strings.NewReader(msg), &dkim.SignOptions{
		Domain: "example.com", Selector: "s1", Signer: key,
		HeaderKeys: []string{"From", "To", "Subject", "Date", "Message-ID"},
	}))

	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)

	return out.Bytes(), "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(pub)
}

func authEmail() *jmap.Email {
	headers := []jmap.EmailHeader{
		hdr("Received", "from relay.internal (relay.internal [10.0.0.4]) by mx.provider.test"),
		hdr("Received", "from mail.example.com (mail.example.com [203.0.113.7]) by relay.internal"),
		hdr("Return-Path", "<alice@example.com>"),
	}

	return &jmap.Email{
		BlobID:  "blob1",
		From:    []jmap.EmailAddress{{Email: "alice@example.com"}},
		Headers: headers,
	}
}

func newAuthHandler(res *stubResolver, raw []byte) *Handler {
	h := &Handler{dnsResolver: res}
	h.fetchBlob = func(context.Context, string) ([]byte, error) { return raw, nil }

	return h
}

func TestEvaluateSPFDKIMDMARCPass(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	raw, dnsKey := signedMessage(t, "hello")
	res := &stubResolver{txt: map[string][]string{
		"example.com":                  {"v=spf1 ip4:203.0.113.7 -all"},
		"s1._domainkey.example.com":    {dnsKey},
		"_dmarc.example.com":           {"v=DMARC1; p=reject"},
		"mail.example.com":             nil,
		"7.113.0.203.zen.spamhaus.org": nil,
	}}

	cfg := &emailconfig.EmailConfig{RequireSPF: "pass", RequireDKIM: true, RequireDMARC: "pass"}
	auth := newAuthHandler(res, raw).evaluateSenderAuth(t.Context(), authEmail(), cfg)

	r.Equal("203.0.113.7", auth.SendingIP)
	r.Equal("pass", auth.SPF)
	r.Equal("example.com", auth.SPFDomain)
	r.Equal("pass", auth.DKIM)
	r.Equal([]string{"example.com"}, auth.DKIMDomains)
	r.True(auth.DKIMAligned)
	r.Equal("pass", auth.DMARC)
	r.Equal("reject", auth.DMARCPolicy)
	r.Empty(auth.Failures)
	r.Equal([]string{"10.0.0.4", "203.0.113.7"}, auth.ReceivedIPs)
}

func TestEvaluateSPFSoftfailFails(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	res := &stubResolver{txt: map[string][]string{"example.com": {"v=spf1 ip4:192.0.2.1 ~all"}}}
	auth := newAuthHandler(res, nil).evaluateSenderAuth(t.Context(), authEmail(),
		&emailconfig.EmailConfig{RequireSPF: "pass"})

	r.Equal("softfail", auth.SPF)
	r.Equal([]string{"SPF softfail for example.com"}, auth.Failures)
	r.Equal("delivered, but SPF softfail for example.com", auth.failureMessage())

	// The same verdict satisfies a "softfail" minimum.
	auth = newAuthHandler(res, nil).evaluateSenderAuth(t.Context(), authEmail(),
		&emailconfig.EmailConfig{RequireSPF: "softfail"})
	r.Empty(auth.Failures)
}

func TestEvaluateDKIMTamperedBodyFails(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	raw, dnsKey := signedMessage(t, "hello")
	raw = bytes.Replace(raw, []byte("hello"), []byte("HELLO"), 1)
	res := &stubResolver{txt: map[string][]string{"s1._domainkey.example.com": {dnsKey}}}

	auth := newAuthHandler(res, raw).evaluateSenderAuth(t.Context(), authEmail(),
		&emailconfig.EmailConfig{RequireDKIM: true})

	r.Equal("fail", auth.DKIM)
	r.False(auth.DKIMAligned)
	r.Equal([]string{"DKIM fail"}, auth.Failures)
}

func TestEvaluateDKIMUnsignedIsNone(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	raw := []byte("From: alice@example.com\r\nSubject: x\r\n\r\nbody\r\n")
	auth := newAuthHandler(&stubResolver{}, raw).evaluateSenderAuth(t.Context(), authEmail(),
		&emailconfig.EmailConfig{RequireDKIM: true})

	r.Equal("none", auth.DKIM)
	r.Len(auth.Failures, 1)
}

func TestEvaluateDMARCFailWhenNothingAligned(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	res := &stubResolver{txt: map[string][]string{
		"example.com":        {"v=spf1 -all"},
		"_dmarc.example.com": {"v=DMARC1; p=quarantine"},
	}}
	raw := []byte("From: alice@example.com\r\n\r\nbody\r\n")

	auth := newAuthHandler(res, raw).evaluateSenderAuth(t.Context(), authEmail(),
		&emailconfig.EmailConfig{RequireDMARC: "pass"})

	r.Equal("fail", auth.SPF)
	r.Equal("fail", auth.DMARC)
	r.Equal("quarantine", auth.DMARCPolicy)
	r.Equal([]string{"DMARC fail"}, auth.Failures)
}

func TestEvaluateBlocklistListedCleanAndRefused(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	res := &stubResolver{hosts: map[string][]string{
		"7.113.0.203.zen.spamhaus.org": {"127.0.0.2"},
		"7.113.0.203.bl.spamcop.net":   {"127.255.255.254"}, // query refused, not a listing
	}}
	cfg := &emailconfig.EmailConfig{Blocklists: []string{"zen.spamhaus.org", "bl.spamcop.net", "b.example.org"}}

	auth := newAuthHandler(res, nil).evaluateSenderAuth(t.Context(), authEmail(), cfg)

	r.Equal([]string{"zen.spamhaus.org"}, auth.BlocklistedOn)
	r.Equal([]string{"sending IP 203.0.113.7 is listed on zen.spamhaus.org"}, auth.Failures)

	clean := newAuthHandler(&stubResolver{}, nil).evaluateSenderAuth(t.Context(), authEmail(), cfg)
	r.Empty(clean.BlocklistedOn)
	r.Empty(clean.Failures)

	// Every zone refusing is inconclusive: reported, never failing.
	refused := newAuthHandler(&stubResolver{hosts: map[string][]string{
		"7.113.0.203.zen.spamhaus.org": {"127.255.255.254"},
	}}, nil).evaluateSenderAuth(t.Context(), authEmail(),
		&emailconfig.EmailConfig{Blocklists: []string{"zen.spamhaus.org"}})
	r.Empty(refused.Failures)
	r.Contains(refused.TempErrors, "blocklists")
}

func TestEvaluateTimeoutIsTempErrorAndNeverFails(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	raw, _ := signedMessage(t, "hello")
	h := newAuthHandler(&stubResolver{block: true}, raw)
	h.authTimeout = 100 * time.Millisecond

	cfg := &emailconfig.EmailConfig{
		RequireSPF: "pass", RequireDKIM: true, RequireDMARC: "pass",
		Blocklists: []string{"zen.spamhaus.org"},
	}

	start := time.Now()
	auth := h.evaluateSenderAuth(t.Context(), authEmail(), cfg)

	r.Less(time.Since(start), 3*time.Second)
	r.Equal("temperror", auth.SPF)
	r.Equal("temperror", auth.DKIM)
	r.Equal("temperror", auth.DMARC)
	r.Empty(auth.Failures, "resolver noise must not fail the check")
	r.NotEmpty(auth.TempErrors)
}

func TestEvaluateMissingRawMessageIsTempError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h := &Handler{dnsResolver: &stubResolver{}}
	auth := h.evaluateSenderAuth(t.Context(), authEmail(), &emailconfig.EmailConfig{RequireDKIM: true})

	r.Equal("temperror", auth.DKIM)
	r.Empty(auth.Failures)
}

func TestSenderAuthOutputOnlyCarriesEvaluatedParts(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	auth := newAuthHandler(&stubResolver{}, nil).evaluateSenderAuth(t.Context(), authEmail(),
		&emailconfig.EmailConfig{CheckBlocklists: true})
	out := auth.output()

	r.Contains(out, "blocklisted_on")
	r.Contains(out, "sending_ip")
	r.NotContains(out, "spf")
	r.NotContains(out, "dkim")
	r.NotContains(out, "dmarc")
}
