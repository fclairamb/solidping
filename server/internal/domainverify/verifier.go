// Package domainverify normalizes customer-owned status-page hostnames and
// verifies routing + ownership over DNS with a SINGLE CNAME record.
//
// Two modes are supported (see Mode):
//
//   - shared: the customer CNAMEs their host at the installation's plain CNAME
//     target (e.g. "status.acme.com CNAME solidping.io"). One record, matching
//     every competitor's UX. Trade-off: a dangling CNAME left pointing at the
//     shared target can be claimed by any org — the global unique index on
//     status_pages.custom_domain (first claim wins) is the only arbiter.
//   - token: the CNAME target is per-page, "<token>.cname.<target>". Same
//     one-record UX, but the target itself carries the per-page secret, so a
//     dangling CNAME can only ever be re-claimed by a page holding that token.
//     Requires a wildcard A/AAAA (or ALIAS) record at "*.cname.<target>" — NOT
//     a CNAME. What LookupCNAME returns for a multi-hop chain is
//     resolver-dependent (see CheckCNAME's doc), so a wildcard CNAME
//     hop may or may not be visible; requiring A/AAAA is the only answer that
//     is correct on every platform.
//
// Verification passes in the configured mode only: accepting the shared target
// while in token mode would nullify the protection.
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

const (
	// TokenHostLabel is the label the per-page token hosts live under in token
	// mode: "<token>.cname.<cnameTarget>". The installation must publish a
	// wildcard A/AAAA (or ALIAS) record for "*.cname.<cnameTarget>".
	TokenHostLabel = "cname"
	// maxDomainLen is the DNS maximum hostname length.
	maxDomainLen = 253
	// maxLabelLen is the DNS maximum single-label length. The token must stay
	// well inside it since it becomes a label of the expected CNAME target.
	maxLabelLen = 63
)

// Mode is the CNAME verification mode (config
// server.custom_domain_cname_mode).
type Mode string

const (
	// ModeShared verifies against the installation's plain CNAME target.
	ModeShared Mode = "shared"
	// ModeToken verifies against a per-page "<token>.cname.<target>" host.
	ModeToken Mode = "token"
)

// Valid reports whether m is one of the supported modes.
func (m Mode) Valid() bool {
	return m == ModeShared || m == ModeToken
}

// ParseMode maps a raw config string to a Mode, defaulting to ModeShared for
// the empty string. An unrecognized value returns ok=false so config validation
// can reject it loudly instead of silently downgrading the protection.
func ParseMode(raw string) (Mode, bool) {
	switch Mode(strings.ToLower(strings.TrimSpace(raw))) {
	case "":
		return ModeShared, true
	case ModeShared:
		return ModeShared, true
	case ModeToken:
		return ModeToken, true
	default:
		return ModeShared, false
	}
}

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
// domain. Only "CNAME" is emitted since v0.8.0 — the TXT ownership challenge
// was removed in favor of the token-mode CNAME target.
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

// TokenHost returns the per-page CNAME target used in token mode:
// "<token>.cname.<cnameTarget>". It returns "" when either input is empty or
// the token cannot be a DNS label (too long / illegal characters), so callers
// never publish or verify against a malformed target.
func TokenHost(token, cnameTarget string) string {
	tok := strings.ToLower(strings.TrimSpace(token))
	target := normalizeTarget(cnameTarget)

	if tok == "" || target == "" || len(tok) > maxLabelLen || !isDNSLabel(tok) {
		return ""
	}

	return tok + "." + TokenHostLabel + "." + target
}

// isDNSLabel reports whether s is a syntactically valid single DNS label
// (letters, digits and internal hyphens).
func isDNSLabel(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") {
		return false
	}

	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return false
		}
	}

	return true
}

// normalizeTarget lowercases and strips a trailing dot / surrounding space from
// a CNAME target.
func normalizeTarget(target string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(target), "."))
}

// ExpectedTarget returns the single CNAME value the customer must publish for a
// page, given the installation's CNAME target, the page's token and the
// configured mode. Returns "" when the target cannot be built (no configured
// CNAME target, or token mode with an unusable token) — callers must treat that
// as "custom domains not configured" and never verify successfully.
func ExpectedTarget(mode Mode, token, cnameTarget string) string {
	if mode == ModeToken {
		return TokenHost(token, cnameTarget)
	}

	return normalizeTarget(cnameTarget)
}

// Records returns the DNS records a customer must create. Since v0.8.0 that is
// exactly one CNAME whose value depends on the configured mode. An empty slice
// is returned when no target can be built.
func Records(domain, token, cnameTarget string, mode Mode) []Record {
	value := ExpectedTarget(mode, token, cnameTarget)
	if value == "" {
		return nil
	}

	return []Record{{Type: "CNAME", Name: domain, Value: value}}
}

// Verifier runs the DNS checks. The lookup func defaults to a plain
// net.Resolver but is injectable for tests.
type Verifier struct {
	LookupCNAME func(ctx context.Context, host string) (string, error)
}

// New returns a Verifier backed by the system resolver.
func New() *Verifier {
	resolver := &net.Resolver{}

	return &Verifier{
		LookupCNAME: resolver.LookupCNAME,
	}
}

// CheckCNAME reports whether the domain's CNAME resolves to the expected target
// (case-insensitive, trailing-dot-stripped). An empty expected target never
// matches. A transport/lookup error (including NXDOMAIN) is returned so callers
// can log it, but always alongside ok=false.
//
// What LookupCNAME returns when the customer's record points at a target that
// is ITSELF a CNAME is resolver-dependent — the misunderstanding that produced
// the false "verification fails while DNS resolves" report investigated in spec
// 2026-08-23-05. The intuition that it yields the FINAL canonical name of the
// chain is wrong for Go's pure resolver: goLookupCNAME delegates to
// goLookupIPCNAMEOrder(ctx, "CNAME", ...), which fires A, AAAA and CNAME
// queries and keeps the FIRST CNAME record it parses. Every recursive answer
// starts its answer section with the queried name's own CNAME, so what comes
// back is the FIRST hop — exactly the target the customer published, chain or
// no chain. The cgo resolver (getaddrinfo with AI_CANONNAME) can legitimately
// return the final name instead, so neither behavior may be relied upon.
//
// Two consequences:
//
//   - An instance CNAME target that is itself a CNAME does NOT, on its own,
//     break verification under the pure-Go resolver used by CGO_ENABLED=0
//     builds. Before blaming "chain flattening", read the recorded Diagnosis
//     line: a resolved= differing from expected= is a real mismatch, an error=
//     is a resolver/transport fault, and an intermittent mix of pass and fail
//     is the latter.
//   - Token mode still demands a wildcard A/AAAA (never a CNAME) at
//     "*.cname.<target>", precisely because the two resolvers disagree.
//
// See wiki/runbooks/custom-domain-tls.md section 7.
func (v *Verifier) CheckCNAME(ctx context.Context, domain, expected string) (bool, error) {
	want := normalizeTarget(expected)
	if want == "" {
		return false, nil
	}

	cname, err := v.LookupCNAME(ctx, domain)
	if err != nil {
		return false, err
	}

	got := normalizeTarget(cname)

	return got == want, nil
}

// Verify reports whether the domain's CNAME points at the mode's expected
// target. Lookup errors count as "not verified". There is deliberately no
// dual-accept: in token mode the plain shared target does NOT verify.
func (v *Verifier) Verify(ctx context.Context, domain, token, cnameTarget string, mode Mode) bool {
	return v.Diagnose(ctx, domain, token, cnameTarget, mode).OK
}

// Diagnosis is everything one verification attempt saw. It exists because
// "verification failed" is not an actionable statement: the same false has
// meant a wrong CNAME, an NXDOMAIN, an installation with no CNAME target
// configured at all, and a page verified in the wrong mode. Telling those apart
// used to require correlating server logs with manual dig runs from inside the
// same network namespace.
type Diagnosis struct {
	// Domain is the hostname that was looked up.
	Domain string
	// Mode is the verification mode in force.
	Mode Mode
	// Expected is the CNAME value the domain had to resolve to. Empty means
	// the installation could not build one (no configured CNAME target, or an
	// unusable token in token mode) — in which case nothing could have
	// verified and the DNS was never consulted.
	Expected string
	// Resolved is the name DNS actually returned, normalized the same way
	// Expected is. Empty when the lookup failed. Which name that is for a
	// multi-hop chain is resolver-dependent — see CheckCNAME's doc.
	Resolved string
	// Err is the lookup transport error (including NXDOMAIN), if any.
	Err error
	// OK is the verdict.
	OK bool
}

// String renders the diagnosis as one log/DB-friendly line.
func (d Diagnosis) String() string {
	expected := d.Expected
	if expected == "" {
		expected = "<none: no CNAME target configured for this mode>"
	}

	resolved := d.Resolved
	if resolved == "" {
		resolved = "<none>"
	}

	line := fmt.Sprintf("mode=%s expected=%s resolved=%s ok=%t", d.Mode, expected, resolved, d.OK)
	if d.Err != nil {
		line += " error=" + d.Err.Error()
	}

	return line
}

// Diagnose runs the same check as Verify and reports everything it saw.
func (v *Verifier) Diagnose(ctx context.Context, domain, token, cnameTarget string, mode Mode) Diagnosis {
	diag := Diagnosis{
		Domain:   domain,
		Mode:     mode,
		Expected: ExpectedTarget(mode, token, cnameTarget),
	}

	if diag.Expected == "" {
		return diag
	}

	cname, err := v.LookupCNAME(ctx, domain)
	if err != nil {
		diag.Err = err

		return diag
	}

	diag.Resolved = normalizeTarget(cname)
	diag.OK = diag.Resolved == normalizeTarget(diag.Expected)

	return diag
}
