package emailcheck

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	emailconfig "github.com/fclairamb/solidping/server/internal/checkers/checkemail/config"
	"github.com/fclairamb/solidping/server/internal/jmap"
)

// tempErrResolver fails every TXT lookup whose name starts with prefix with a
// temporary DNS error, and defers to the embedded stub otherwise.
type tempErrResolver struct {
	*stubResolver
	prefix string
}

func (r *tempErrResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	if strings.HasPrefix(name, r.prefix) {
		return nil, &net.DNSError{Err: "servfail", Name: name, IsTemporary: true}
	}

	return r.stubResolver.LookupTXT(ctx, name)
}

func TestParseReceivedSkipsUnusableHeaders(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	hops := parseReceived([]jmap.EmailHeader{
		hdr("Subject", "from a ([1.2.3.4])"),
		hdr("Received", "by mx.provider.test with ESMTP"),                   // no "from"
		hdr("Received", "from a.example by mx.provider.test"),               // no IP
		hdr("Received", "from a.example ([999.1.1.1]) by mx.provider.test"), // unparsable IP
		hdr("Received", "from ok.example (ok.example [198.51.100.5]) by mx"),
	})

	r.Len(hops, 1)
	r.Equal("198.51.100.5", hops[0].ip)
}

func TestHeaderFromDomainAndDomainOf(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Empty(headerFromDomain(&jmap.Email{}))
	r.Equal("example.com", headerFromDomain(&jmap.Email{From: []jmap.EmailAddress{{Email: "A@Example.COM"}}}))
	r.Empty(domainOf("no-at-sign"))
	r.Equal("example.com", domainOf(" x@example.com "))
}

func TestEnvelopeSenderFallbacks(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	email := &jmap.Email{From: []jmap.EmailAddress{{Email: "alice@from.example"}}}

	// Return-Path wins.
	withPath := &jmap.Email{Headers: []jmap.EmailHeader{hdr("Return-Path", "<bounce@rp.example>")}}
	addr, domain := envelopeSender(withPath, receivedHop{helo: "mail.helo.example"})
	r.Equal("bounce@rp.example", addr)
	r.Equal("rp.example", domain)

	// An unparsable Return-Path falls back to the HELO of the trusted hop.
	bad := &jmap.Email{Headers: []jmap.EmailHeader{hdr("Return-Path", "<<>")}}
	addr, domain = envelopeSender(bad, receivedHop{helo: "Mail.Helo.Example"})
	r.Equal("postmaster@Mail.Helo.Example", addr)
	r.Equal("mail.helo.example", domain)

	// No usable HELO (no dot): the From domain.
	addr, domain = envelopeSender(email, receivedHop{helo: "localhost"})
	r.Equal("postmaster@from.example", addr)
	r.Equal("from.example", domain)
}

func TestResolverDefaultsToSystemResolver(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, ok := (&Handler{}).resolver().(*net.Resolver)
	r.True(ok)

	stub := &stubResolver{}
	r.Same(stub, (&Handler{dnsResolver: stub}).resolver())
}

func TestEvaluateSPFWithoutPublicHopIsNone(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	email := authEmail()
	email.Headers = []jmap.EmailHeader{hdr("Received", "from a (a [10.1.1.1]) by b")}

	auth := newAuthHandler(&stubResolver{}, nil).evaluateSenderAuth(t.Context(), email,
		&emailconfig.EmailConfig{RequireSPF: "pass"})

	r.Empty(auth.SendingIP)
	r.Equal("none", auth.SPF)
	r.Equal([]string{"SPF none for "}, auth.Failures)
}

func TestEvaluateSPFTempErrorNeverFails(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	res := &tempErrResolver{stubResolver: &stubResolver{}, prefix: "example.com"}
	h := &Handler{dnsResolver: res}

	auth := h.evaluateSenderAuth(t.Context(), authEmail(), &emailconfig.EmailConfig{RequireSPF: "pass"})

	r.Equal("temperror", auth.SPF)
	r.Empty(auth.Failures)
	r.Contains(auth.TempErrors, "spf")
}

func TestEvaluateDKIMUnreadableMessageFails(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	auth := newAuthHandler(&stubResolver{}, []byte("")).evaluateSenderAuth(t.Context(), authEmail(),
		&emailconfig.EmailConfig{RequireDKIM: true})

	r.Contains([]string{"fail", "none"}, auth.DKIM)
	r.Len(auth.Failures, 1)
}

func TestEvaluateDKIMKeyLookupTempFailIsTempError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	raw, _ := signedMessage(t, "hello")
	h := newAuthHandler(nil, raw)
	h.dnsResolver = &tempErrResolver{stubResolver: &stubResolver{}, prefix: "s1._domainkey"}

	auth := h.evaluateSenderAuth(t.Context(), authEmail(), &emailconfig.EmailConfig{RequireDKIM: true})

	r.Equal("temperror", auth.DKIM)
	r.Empty(auth.Failures)
	r.Contains(auth.TempErrors, "dkim")
}

func TestEvaluateDKIMNotAlignedFails(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	raw, dnsKey := signedMessage(t, "hello other")
	res := &stubResolver{txt: map[string][]string{"s1._domainkey.example.com": {dnsKey}}}

	email := authEmail()
	email.From = []jmap.EmailAddress{{Email: "alice@other.org"}}

	auth := newAuthHandler(res, raw).evaluateSenderAuth(t.Context(), email,
		&emailconfig.EmailConfig{RequireDKIM: true})

	r.Equal("pass", auth.DKIM)
	r.False(auth.DKIMAligned)
	r.Equal([]string{"DKIM signature not aligned with the From domain"}, auth.Failures)
}

func TestEvaluateDMARCFallsBackToOrganizationalDomain(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	res := &stubResolver{txt: map[string][]string{
		"example.com":        {"v=spf1 ip4:203.0.113.7 -all"},
		"_dmarc.example.com": {"v=DMARC1; p=none"},
	}}

	email := authEmail()
	email.From = []jmap.EmailAddress{{Email: "alice@news.example.com"}}

	auth := newAuthHandler(res, nil).evaluateSenderAuth(t.Context(), email,
		&emailconfig.EmailConfig{RequireDMARC: "pass"})

	r.Equal("pass", auth.SPF)
	r.Equal("pass", auth.DMARC)
	r.Equal("none", auth.DMARCPolicy)
	r.Empty(auth.Failures)
}

func TestEvaluateDMARCStrictAlignmentRejectsSubdomain(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	res := &stubResolver{txt: map[string][]string{
		"example.com":        {"v=spf1 ip4:203.0.113.7 -all"},
		"_dmarc.example.com": {"v=DMARC1; p=reject; aspf=s"},
	}}

	email := authEmail()
	email.From = []jmap.EmailAddress{{Email: "alice@example.com"}}
	email.Headers = append(email.Headers[:2:2], hdr("Return-Path", "<bounce@sub.example.com>"))

	res.txt["sub.example.com"] = []string{"v=spf1 ip4:203.0.113.7 -all"}

	auth := newAuthHandler(res, nil).evaluateSenderAuth(t.Context(), email,
		&emailconfig.EmailConfig{RequireDMARC: "pass"})

	r.Equal("pass", auth.SPF)
	r.Equal("sub.example.com", auth.SPFDomain)
	r.NotEqual("pass", auth.DMARC)
	r.Equal([]string{"DMARC " + auth.DMARC}, auth.Failures)
}

func TestEvaluateDMARCEdgeCases(t *testing.T) {
	t.Parallel()

	cfg := &emailconfig.EmailConfig{RequireDMARC: "pass"}

	t.Run("no From domain is none", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		email := authEmail()
		email.From = nil

		auth := newAuthHandler(&stubResolver{}, nil).evaluateSenderAuth(t.Context(), email, cfg)
		r.Equal("none", auth.DMARC)
		r.Equal([]string{"DMARC none"}, auth.Failures)
	})

	t.Run("unparsable policy is none", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		res := &stubResolver{txt: map[string][]string{"_dmarc.example.com": {"v=DMARC1; p=bogus"}}}
		auth := newAuthHandler(res, nil).evaluateSenderAuth(t.Context(), authEmail(), cfg)
		r.Equal("none", auth.DMARC)
	})

	t.Run("policy lookup temp failure is temperror", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		h := newAuthHandler(nil, nil)
		h.dnsResolver = &tempErrResolver{stubResolver: &stubResolver{}, prefix: "_dmarc"}

		auth := h.evaluateSenderAuth(t.Context(), authEmail(), cfg)
		r.Equal("temperror", auth.DMARC)
		r.Empty(auth.Failures)
		r.Contains(auth.TempErrors, "dmarc")
	})

	t.Run("failing SPF with unverifiable DKIM is temperror", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		res := &stubResolver{txt: map[string][]string{
			"example.com":        {"v=spf1 -all"},
			"_dmarc.example.com": {"v=DMARC1; p=reject"},
		}}
		// No raw message: DKIM is temperror, so DMARC cannot be decided.
		auth := (&Handler{dnsResolver: res}).evaluateSenderAuth(t.Context(), authEmail(), cfg)
		r.Equal("fail", auth.SPF)
		r.Equal("temperror", auth.DMARC)
		r.Empty(auth.Failures)
	})
}

func TestAligned(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.False(aligned("", "example.com", ""))
	r.False(aligned("example.com", "", ""))
	r.True(aligned("Example.com", "example.COM", "s"))
	r.True(aligned("mail.example.com", "example.com", ""))
	r.False(aligned("mail.example.com", "example.com", "s"))
	r.False(aligned("a.example.com", "b.example.org", ""))
	r.False(aligned("localhost", "example.com", ""))
}

func TestCollectFailuresDeadlineWithoutOtherTempError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	a := &senderAuth{}
	a.collectFailures(&emailconfig.EmailConfig{}, true)
	r.Equal([]string{"deadline exceeded"}, a.TempErrors)
	r.Empty(a.Failures)
}

func TestSenderAuthOutputFullAndDKIMFailure(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	a := &senderAuth{
		SendingIP: "203.0.113.7", SPF: "pass", SPFDomain: "example.com",
		DKIM: "fail", DMARC: "pass", DMARCPolicy: "reject",
		ProviderAuth: map[string]string{"spf": "pass"},
	}
	a.ran.spf, a.ran.dkim, a.ran.dmarc, a.ran.blocklists = true, true, true, true

	out := a.output()
	r.Equal("example.com", out["spf_domain"])
	r.Equal("reject", out["dmarc_policy"])
	r.Equal([]string{}, out["dkim_domains"])
	r.Equal([]string{}, out["blocklisted_on"])
	r.Equal(map[string]string{"spf": "pass"}, out["provider_auth"])
	r.Equal("DKIM fail", a.dkimFailure())
}

func TestLookupTXTReportsCanceledContextAsTemporary(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := lookupTXT(ctx, &stubResolver{block: true}, "example.com")

	var dnsErr *net.DNSError
	r.ErrorAs(err, &dnsErr)
	r.True(dnsErr.IsTemporary)
	r.True(dnsErr.IsTimeout)

	_, err = lookupTXT(t.Context(), &stubResolver{}, "example.com")
	r.ErrorAs(err, &dnsErr)
	r.True(dnsErr.IsNotFound)
}

func TestEvaluateWithoutAnyReceivedHeader(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	email := authEmail()
	email.Headers = []jmap.EmailHeader{hdr("Authentication-Results", "mx.provider.test; none")}

	auth := newAuthHandler(&stubResolver{}, nil).evaluateSenderAuth(t.Context(), email,
		&emailconfig.EmailConfig{CheckBlocklists: true})

	r.Equal([]string{}, auth.ReceivedIPs)
	r.Empty(auth.SendingIP)
	r.Empty(auth.BlocklistedOn)
	r.Empty(auth.Failures)
	r.Nil(auth.ProviderAuth, "a header without spf/dkim/dmarc results carries nothing")
}

func TestEvaluateSPFUsesSenderDomainWhenHeloIsEmpty(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	email := authEmail()
	email.Headers = []jmap.EmailHeader{
		hdr("Received", "from [] ([203.0.113.7]) by mx.provider.test"),
		hdr("Return-Path", "<alice@example.com>"),
	}
	res := &stubResolver{txt: map[string][]string{"example.com": {"v=spf1 ip4:203.0.113.7 -all"}}}

	auth := newAuthHandler(res, nil).evaluateSenderAuth(t.Context(), email,
		&emailconfig.EmailConfig{RequireSPF: "pass"})

	r.Equal("pass", auth.SPF)
	r.Empty(auth.Failures)
}

func TestEvaluateDMARCWithoutAnyPolicyIsNone(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	auth := (&Handler{dnsResolver: &stubResolver{}}).evaluateSenderAuth(t.Context(), authEmail(),
		&emailconfig.EmailConfig{RequireDMARC: "pass"})

	r.Equal("none", auth.DMARC)
	r.Empty(auth.DMARCPolicy)
	r.Equal([]string{"DMARC none"}, auth.Failures)
}

func TestEvaluateDKIMUnparsableMessageAfterDeadlineIsTempError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h := newAuthHandler(&stubResolver{}, []byte("this is not a mail\r\n"))
	h.authTimeout = time.Nanosecond

	auth := h.evaluateSenderAuth(t.Context(), authEmail(), &emailconfig.EmailConfig{RequireDKIM: true})

	r.Equal("temperror", auth.DKIM)
	r.Empty(auth.Failures)
}
