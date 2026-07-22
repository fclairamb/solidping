// Package domainverify normalizes customer-owned status-page hostnames and
// verifies ownership + routing over DNS. Ownership is proven by a TXT challenge
// (`_solidping-challenge.<domain>` = `sp-domain-verify=<token>`); routing is
// proven by a CNAME of `<domain>` pointing at the installation's configured
// CNAME target. Both are required to serve a page on a custom host.
//
// The resolver funcs are injectable so handlers and jobs can be unit-tested
// without real DNS, mirroring the net.Resolver idiom in checkers/checkdns.
package domainverify

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"golang.org/x/net/idna"
)

// DNS challenge naming. The TXT record lives at ChallengeLabel + "." + domain
// and its value is TXTValuePrefix + token.
const (
	// ChallengeLabel is the sub-label the ownership TXT record is published at.
	ChallengeLabel = "_solidping-challenge"
	// TXTValuePrefix prefixes the token inside the challenge TXT record.
	TXTValuePrefix = "sp-domain-verify="
	// maxDomainLen is the DNS maximum hostname length.
	maxDomainLen = 253
)

// Validation errors returned by Normalize. All wrap ErrInvalidDomain so callers
// can map any of them to a single VALIDATION_ERROR.
var (
	// ErrInvalidDomain is the umbrella error for a hostname that cannot be a
	// custom domain.
	ErrInvalidDomain = errors.New("invalid custom domain")
	errEmptyDomain   = fmt.Errorf("%w: empty", ErrInvalidDomain)
	errDomainHasPort = fmt.Errorf("%w: must not include a port", ErrInvalidDomain)
	errDomainIsIP    = fmt.Errorf("%w: must be a hostname, not an IP address", ErrInvalidDomain)
	errDomainWild    = fmt.Errorf("%w: wildcards are not allowed", ErrInvalidDomain)
	errDomainLabels  = fmt.Errorf("%w: must have at least two labels", ErrInvalidDomain)
	errDomainTooLong = fmt.Errorf("%w: too long", ErrInvalidDomain)
)

// Record is one DNS record the customer must create to activate a custom
// domain. Type is "CNAME" or "TXT".
type Record struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Normalize lowercases, strips a trailing dot, converts IDN to ASCII/punycode
// (storing only the ASCII form), and validates that the result is a plausible
// custom-domain hostname: at least two labels, no port, no wildcard, not an IP.
func Normalize(raw string) (string, error) {
	domain := strings.TrimSpace(raw)
	domain = strings.TrimSuffix(domain, ".")
	domain = strings.ToLower(domain)

	if domain == "" {
		return "", errEmptyDomain
	}

	// Reject anything carrying a scheme, path, port, or wildcard before we hand
	// it to idna (which would otherwise mangle or accept some of these).
	if strings.Contains(domain, "/") || strings.Contains(domain, ":") {
		return "", errDomainHasPort
	}

	if strings.Contains(domain, "*") {
		return "", errDomainWild
	}

	if net.ParseIP(domain) != nil {
		return "", errDomainIsIP
	}

	// idna.Lookup validates label syntax and produces the ASCII/punycode form.
	ascii, err := idna.Lookup.ToASCII(domain)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidDomain, err.Error())
	}

	if len(ascii) > maxDomainLen {
		return "", errDomainTooLong
	}

	labels := strings.Split(ascii, ".")
	if len(labels) < 2 {
		return "", errDomainLabels
	}

	for _, label := range labels {
		if label == "" {
			return "", errDomainLabels
		}
	}

	// A bare IP could survive as a "hostname" that is actually numeric — reject
	// the ASCII form too for safety.
	if net.ParseIP(ascii) != nil {
		return "", errDomainIsIP
	}

	return ascii, nil
}

// ChallengeName returns the fully-qualified name of the ownership TXT record for
// a (normalized) domain.
func ChallengeName(domain string) string {
	return ChallengeLabel + "." + domain
}

// Records returns the two DNS records a customer must create: the CNAME that
// routes the domain to the installation and the TXT challenge that proves
// ownership.
func Records(domain, token, cnameTarget string) []Record {
	return []Record{
		{Type: "CNAME", Name: domain, Value: cnameTarget},
		{Type: "TXT", Name: ChallengeName(domain), Value: TXTValuePrefix + token},
	}
}

// Verifier runs the DNS checks. The lookup funcs default to a plain
// net.Resolver but are injectable for tests.
type Verifier struct {
	LookupTXT   func(ctx context.Context, name string) ([]string, error)
	LookupCNAME func(ctx context.Context, host string) (string, error)
}

// New returns a Verifier backed by the system resolver.
func New() *Verifier {
	resolver := &net.Resolver{}

	return &Verifier{
		LookupTXT:   resolver.LookupTXT,
		LookupCNAME: resolver.LookupCNAME,
	}
}

// CheckTXT reports whether the ownership TXT challenge is published for the
// domain. A transport/lookup error (including NXDOMAIN) is returned so callers
// can log it, but always alongside ok=false.
func (v *Verifier) CheckTXT(ctx context.Context, domain, token string) (bool, error) {
	records, err := v.LookupTXT(ctx, ChallengeName(domain))
	if err != nil {
		return false, err
	}

	expected := TXTValuePrefix + token

	for _, record := range records {
		if strings.TrimSpace(record) == expected {
			return true, nil
		}
	}

	return false, nil
}

// CheckCNAME reports whether the domain's CNAME resolves to the configured
// target (case-insensitive, trailing-dot-stripped).
func (v *Verifier) CheckCNAME(ctx context.Context, domain, cnameTarget string) (bool, error) {
	cname, err := v.LookupCNAME(ctx, domain)
	if err != nil {
		return false, err
	}

	got := strings.ToLower(strings.TrimSuffix(cname, "."))
	want := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(cnameTarget), "."))

	return want != "" && got == want, nil
}

// Verify reports whether the domain passes both ownership (TXT) and routing
// (CNAME). Lookup errors count as "not verified".
func (v *Verifier) Verify(ctx context.Context, domain, token, cnameTarget string) bool {
	txtOK, _ := v.CheckTXT(ctx, domain, token)
	if !txtOK {
		return false
	}

	cnameOK, _ := v.CheckCNAME(ctx, domain, cnameTarget)

	return cnameOK
}
