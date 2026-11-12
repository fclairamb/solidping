package emailcheck

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"time"

	"blitiri.com.ar/go/spf"
	"github.com/emersion/go-msgauth/dkim"
	"github.com/emersion/go-msgauth/dmarc"
	"golang.org/x/net/publicsuffix"

	dnsblconfig "github.com/fclairamb/solidping/server/internal/checkers/checkdnsbl/config"
	emailconfig "github.com/fclairamb/solidping/server/internal/checkers/checkemail/config"
	"github.com/fclairamb/solidping/server/internal/checkers/dnsblquery"
	"github.com/fclairamb/solidping/server/internal/jmap"
)

// authTimeout bounds the whole sender-authentication evaluation: it runs inside
// the JMAP manager's sync loop, so an unbounded DNS lookup would delay every
// other email of the batch.
const defaultAuthTimeout = 5 * time.Second

// Verdict values (RFC 8601 vocabulary). temperror never fails a requirement.
const (
	verdictPass      = "pass"
	verdictFail      = "fail"
	verdictNone      = "none"
	verdictTempError = "temperror"
)

const headerAuthResults = "Authentication-Results"

var (
	errNoRawMessage = errors.New("raw message unavailable")

	receivedFromPattern = regexp.MustCompile(`(?is)^\s*from\s+(\S+)(.*?)(?:\sby\s|$)`)
	receivedIPPattern   = regexp.MustCompile(`[\[(](?:IPv6:)?([0-9A-Fa-f:.]+)[\])]`)
	providerAuthPattern = regexp.MustCompile(`(?i)(?:^|[\s;])(spf|dkim|dmarc)\s*=\s*([a-z]+)`)
)

// authResolver is every DNS lookup the evaluation performs. *net.Resolver
// satisfies it; tests inject a stub.
type authResolver interface {
	spf.DNSResolver
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// blobFetcher downloads the raw RFC 5322 message behind a blobId.
type blobFetcher func(ctx context.Context, blobID string) ([]byte, error)

// senderAuth is the outcome of evaluateSenderAuth: the verdicts that go into the
// result output and the requirements that failed.
type senderAuth struct {
	SendingIP     string
	SPF           string
	SPFDomain     string
	DKIM          string
	DKIMDomains   []string
	DKIMAligned   bool
	DMARC         string
	DMARCPolicy   string
	BlocklistedOn []string
	ProviderAuth  map[string]string
	ReceivedIPs   []string

	// Failures says which requirements failed, in the order they are reported.
	Failures []string
	// TempErrors lists the evaluations that could not complete (logged at WARN).
	TempErrors []string
	// ran records which parts were evaluated, so the output only carries those.
	ran struct{ spf, dkim, dmarc, blocklists bool }
}

// output renders the "auth" object of the result.
func (a *senderAuth) output() map[string]any {
	out := map[string]any{"received_ips": a.ReceivedIPs}

	if a.SendingIP != "" {
		out["sending_ip"] = a.SendingIP
	}

	if a.ran.spf {
		out["spf"] = a.SPF
		if a.SPFDomain != "" {
			out["spf_domain"] = a.SPFDomain
		}
	}

	if a.ran.dkim {
		out["dkim"] = a.DKIM
		out["dkim_domains"] = nonNil(a.DKIMDomains)
		out["dkim_aligned"] = a.DKIMAligned
	}

	if a.ran.dmarc {
		out["dmarc"] = a.DMARC
		if a.DMARCPolicy != "" {
			out["dmarc_policy"] = a.DMARCPolicy
		}
	}

	if a.ran.blocklists {
		out["blocklisted_on"] = nonNil(a.BlocklistedOn)
	}

	if len(a.ProviderAuth) > 0 {
		out["provider_auth"] = a.ProviderAuth
	}

	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}

// failureMessage is the result message when a requirement failed.
func (a *senderAuth) failureMessage() string {
	return "delivered, but " + strings.Join(a.Failures, "; ")
}

// evaluateSenderAuth checks who sent the message, "like a receiving MTA would".
// SolidPing reads the inbox over JMAP, so the SMTP connection is gone: the
// sending IP and the envelope sender are recovered from the headers the
// receiving server added. Everything is bounded by authTimeout; a lookup that
// cannot finish yields temperror, which never fails a requirement.
func (h *Handler) evaluateSenderAuth(
	ctx context.Context, email *jmap.Email, cfg *emailconfig.EmailConfig,
) *senderAuth {
	ctx, cancel := context.WithTimeout(ctx, h.authDeadline())
	defer cancel()

	auth := &senderAuth{ProviderAuth: parseProviderAuth(email.Headers)}

	hops := parseReceived(email.Headers)
	for _, hop := range hops {
		auth.ReceivedIPs = append(auth.ReceivedIPs, hop.ip)
	}

	if auth.ReceivedIPs == nil {
		auth.ReceivedIPs = []string{}
	}

	hop := trustedHop(hops)
	auth.SendingIP = hop.ip

	fromDomain := headerFromDomain(email)
	needSPF := cfg.RequireSPF != "" || cfg.RequireDMARC != ""
	needDKIM := cfg.RequireDKIM || cfg.RequireDMARC != ""

	var wg sync.WaitGroup

	run := func(enabled bool, fn func()) {
		if !enabled {
			return
		}

		wg.Add(1)

		go func() {
			defer wg.Done()

			fn()
		}()
	}

	run(needSPF, func() { h.evalSPF(ctx, auth, email, hop) })
	run(needDKIM, func() { h.evalDKIM(ctx, auth, email, fromDomain) })
	run(cfg.BlocklistsEnabled(), func() { h.evalBlocklists(ctx, auth, cfg) })

	wg.Wait()

	if cfg.RequireDMARC != "" {
		h.evalDMARC(ctx, auth, fromDomain)
	}

	auth.collectFailures(cfg, ctx.Err() != nil)

	return auth
}

func (h *Handler) authDeadline() time.Duration {
	if h.authTimeout > 0 {
		return h.authTimeout
	}

	return defaultAuthTimeout
}

func (h *Handler) resolver() authResolver {
	if h.dnsResolver != nil {
		return h.dnsResolver
	}

	return &net.Resolver{}
}

// receivedHop is one parsed Received: header.
type receivedHop struct {
	helo string
	ip   string
}

// parseReceived extracts, top-down, the "from <helo> ([ip])" of every Received
// header that names a parseable peer IP.
func parseReceived(headers []jmap.EmailHeader) []receivedHop {
	var hops []receivedHop

	for i := range headers {
		if !strings.EqualFold(strings.TrimSpace(headers[i].Name), "Received") {
			continue
		}

		m := receivedFromPattern.FindStringSubmatch(headers[i].Value)
		if m == nil {
			continue
		}

		ipMatch := receivedIPPattern.FindStringSubmatch(m[2])
		if ipMatch == nil {
			continue
		}

		if net.ParseIP(ipMatch[1]) == nil {
			continue
		}

		hops = append(hops, receivedHop{helo: strings.Trim(m[1], "[]"), ip: net.ParseIP(ipMatch[1]).String()})
	}

	return hops
}

// trustedHop picks the hop the verdict is based on: walking Received headers
// top-down, it skips the hops whose peer is a private or loopback address (the
// provider's own internal relays) and takes the first public one. Hops below it
// were written by the sender's side and can be forged, so they never decide
// anything: they are only reported as received_ips.
func trustedHop(hops []receivedHop) receivedHop {
	for _, hop := range hops {
		ip := net.ParseIP(hop.ip)
		if ip == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			continue
		}

		return hop
	}

	return receivedHop{}
}

// parseProviderAuth reads the first Authentication-Results header (the one the
// receiving provider added) for its spf / dkim / dmarc results. Informational.
func parseProviderAuth(headers []jmap.EmailHeader) map[string]string {
	value := findHeader(headers, headerAuthResults)
	if value == "" {
		return nil
	}

	out := make(map[string]string)

	for _, m := range providerAuthPattern.FindAllStringSubmatch(value, -1) {
		if _, seen := out[strings.ToLower(m[1])]; !seen {
			out[strings.ToLower(m[1])] = strings.ToLower(m[2])
		}
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

func headerFromDomain(email *jmap.Email) string {
	if len(email.From) == 0 {
		return ""
	}

	return domainOf(email.From[0].Email)
}

func domainOf(addr string) string {
	idx := strings.LastIndex(addr, "@")
	if idx < 0 {
		return ""
	}

	return strings.ToLower(strings.TrimSpace(addr[idx+1:]))
}

// returnPathDomainAndSender returns the envelope sender (Return-Path) with its
// domain. Fallbacks: the HELO of the trusted hop, then the From domain.
func envelopeSender(email *jmap.Email, hop receivedHop) (string, string) {
	if rp := findHeader(email.Headers, "Return-Path"); rp != "" {
		if addr, err := mail.ParseAddress(rp); err == nil && domainOf(addr.Address) != "" {
			return addr.Address, domainOf(addr.Address)
		}
	}

	if hop.helo != "" && strings.Contains(hop.helo, ".") {
		return "postmaster@" + hop.helo, strings.ToLower(hop.helo)
	}

	from := headerFromDomain(email)

	return "postmaster@" + from, from
}

func (h *Handler) evalSPF(ctx context.Context, auth *senderAuth, email *jmap.Email, hop receivedHop) {
	auth.ran.spf = true

	ip := net.ParseIP(hop.ip)
	if ip == nil {
		auth.SPF = verdictNone

		return
	}

	sender, domain := envelopeSender(email, hop)
	auth.SPFDomain = domain

	helo := hop.helo
	if helo == "" {
		helo = domain
	}

	res, err := spf.CheckHostWithSender(ip, helo, sender,
		spf.WithContext(ctx), spf.WithResolver(h.resolver()))
	if err != nil && (res == spf.TempError || ctx.Err() != nil) {
		auth.SPF = verdictTempError
		auth.TempErrors = append(auth.TempErrors, "spf")

		return
	}

	auth.SPF = strings.ToLower(string(res))
}

func (h *Handler) evalDKIM(ctx context.Context, auth *senderAuth, email *jmap.Email, fromDomain string) {
	auth.ran.dkim = true

	raw, err := h.rawMessage(ctx, email)
	if err != nil {
		auth.DKIM = verdictTempError
		auth.TempErrors = append(auth.TempErrors, "dkim: "+err.Error())

		return
	}

	res := h.resolver()
	verifs, err := dkim.VerifyWithOptions(bytes.NewReader(raw), &dkim.VerifyOptions{
		MaxVerifications: 5,
		LookupTXT: func(domain string) ([]string, error) {
			return lookupTXT(ctx, res, domain)
		},
	})

	if err != nil && !errors.Is(err, dkim.ErrTooManySignatures) {
		auth.DKIM = verdictFail
		if ctx.Err() != nil {
			auth.DKIM = verdictTempError
			auth.TempErrors = append(auth.TempErrors, "dkim")
		}

		return
	}

	auth.DKIM = classifyDKIM(auth, verifs, fromDomain, ctx.Err() != nil)
}

func classifyDKIM(auth *senderAuth, verifs []*dkim.Verification, fromDomain string, expired bool) string {
	if len(verifs) == 0 {
		return verdictNone
	}

	temp := false

	for _, v := range verifs {
		if v.Err == nil {
			auth.DKIMDomains = append(auth.DKIMDomains, strings.ToLower(v.Domain))

			if aligned(v.Domain, fromDomain, "") {
				auth.DKIMAligned = true
			}

			continue
		}

		if dkim.IsTempFail(v.Err) || expired {
			temp = true
		}
	}

	switch {
	case len(auth.DKIMDomains) > 0:
		return verdictPass
	case temp:
		auth.TempErrors = append(auth.TempErrors, "dkim")

		return verdictTempError
	default:
		return verdictFail
	}
}

func (h *Handler) evalBlocklists(ctx context.Context, auth *senderAuth, cfg *emailconfig.EmailConfig) {
	auth.ran.blocklists = true

	if auth.SendingIP == "" {
		return
	}

	zones := cfg.Blocklists
	if len(zones) == 0 {
		zones = dnsblconfig.DefaultBlocklists
	}

	res := dnsblquery.Query(ctx, h.resolver(), auth.SendingIP, zones)
	auth.BlocklistedOn = res.Listed

	if len(res.Listed) == 0 && len(res.Clean) == 0 && len(res.Inconclusive) > 0 {
		auth.TempErrors = append(auth.TempErrors, "blocklists")
	}
}

func (h *Handler) evalDMARC(ctx context.Context, auth *senderAuth, fromDomain string) {
	auth.ran.dmarc = true

	if fromDomain == "" {
		auth.DMARC = verdictNone

		return
	}

	res := h.resolver()
	opts := &dmarc.LookupOptions{LookupTXT: func(domain string) ([]string, error) {
		return lookupTXT(ctx, res, domain)
	}}

	rec, err := dmarc.LookupWithOptions(fromDomain, opts)
	if errors.Is(err, dmarc.ErrNoPolicy) {
		if org, orgErr := publicsuffix.EffectiveTLDPlusOne(fromDomain); orgErr == nil && org != fromDomain {
			rec, err = dmarc.LookupWithOptions(org, opts)
		}
	}

	switch {
	case errors.Is(err, dmarc.ErrNoPolicy):
		auth.DMARC = verdictNone

		return
	case err != nil:
		auth.DMARC = verdictTempError
		if !dmarc.IsTempFail(err) && ctx.Err() == nil {
			auth.DMARC = verdictNone // unparsable record: as good as none
		} else {
			auth.TempErrors = append(auth.TempErrors, "dmarc")
		}

		return
	}

	auth.DMARCPolicy = string(rec.Policy)

	spfAligned := auth.SPF == verdictPass && aligned(auth.SPFDomain, fromDomain, string(rec.SPFAlignment))

	dkimAligned := false

	for _, d := range auth.DKIMDomains {
		if aligned(d, fromDomain, string(rec.DKIMAlignment)) {
			dkimAligned = true
		}
	}

	switch {
	case spfAligned || dkimAligned:
		auth.DMARC = verdictPass
	case auth.SPF == verdictTempError || auth.DKIM == verdictTempError:
		auth.DMARC = verdictTempError
	default:
		auth.DMARC = verdictFail
	}
}

// aligned reports whether two domains are aligned: equal in strict mode, sharing
// the organisational domain otherwise (relaxed, the default).
func aligned(a, b, mode string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if a == "" || b == "" {
		return false
	}

	if a == b {
		return true
	}

	if mode == string(dmarc.AlignmentStrict) {
		return false
	}

	orgA, errA := publicsuffix.EffectiveTLDPlusOne(a)
	orgB, errB := publicsuffix.EffectiveTLDPlusOne(b)

	return errA == nil && errB == nil && orgA == orgB
}

// collectFailures turns the verdicts into failed requirements. temperror and
// inconclusive blocklist answers never fail anything.
func (a *senderAuth) collectFailures(cfg *emailconfig.EmailConfig, expired bool) {
	if len(a.BlocklistedOn) > 0 {
		a.Failures = append(a.Failures, fmt.Sprintf("sending IP %s is listed on %s",
			a.SendingIP, strings.Join(a.BlocklistedOn, ", ")))
	}

	if cfg.RequireSPF != "" && a.SPF != verdictTempError && !cfg.SPFAccepts(a.SPF) {
		a.Failures = append(a.Failures, fmt.Sprintf("SPF %s for %s", a.SPF, a.SPFDomain))
	}

	if cfg.RequireDKIM && a.DKIM != verdictTempError && !(a.DKIM == verdictPass && a.DKIMAligned) {
		a.Failures = append(a.Failures, a.dkimFailure())
	}

	if cfg.RequireDMARC != "" && a.DMARC != verdictTempError && a.DMARC != verdictPass {
		a.Failures = append(a.Failures, "DMARC "+a.DMARC)
	}

	if expired && len(a.TempErrors) == 0 {
		a.TempErrors = append(a.TempErrors, "deadline exceeded")
	}
}

func (a *senderAuth) dkimFailure() string {
	if a.DKIM == verdictPass {
		return "DKIM signature not aligned with the From domain"
	}

	return "DKIM " + a.DKIM
}

// rawMessage returns the full RFC 5322 message DKIM needs.
func (h *Handler) rawMessage(ctx context.Context, email *jmap.Email) ([]byte, error) {
	if h.fetchBlob == nil || email.BlobID == "" {
		return nil, errNoRawMessage
	}

	return h.fetchBlob(ctx, email.BlobID)
}

// lookupTXT adapts a context-aware resolver to the context-free callbacks of
// go-msgauth. A cancelled context is reported as a temporary DNS error so the
// verdict becomes temperror, not fail.
func lookupTXT(ctx context.Context, res authResolver, domain string) ([]string, error) {
	records, err := res.LookupTXT(ctx, domain)
	if err != nil && ctx.Err() != nil {
		return nil, &net.DNSError{Err: ctx.Err().Error(), Name: domain, IsTimeout: true, IsTemporary: true}
	}

	return records, err
}
