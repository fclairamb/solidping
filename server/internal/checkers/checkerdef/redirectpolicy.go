package checkerdef

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
)

// DefaultMaxRedirects is the longest chain of previous requests a redirect
// policy lets grow before it stops: the HTTP check's historical cap.
const DefaultMaxRedirects = 10

// OutputKeyRedirectDroppedSecretHeaders lists the header names a redirect
// policy withheld from a cross-origin hop (names only, never the values).
const OutputKeyRedirectDroppedSecretHeaders = "redirect_dropped_secret_headers"

// ErrTooManyRedirects ends a request whose redirect chain hit the policy cap.
var ErrTooManyRedirects = errors.New("too many redirects")

// credentialHeaders are always withheld from a cross-origin hop: net/http keeps
// Authorization and Cookie on a same-host scheme or port change (an https ->
// http hop would resend them in cleartext), and Proxy-Authorization is never
// meant for another origin.
//
//nolint:gochecknoglobals // read-only lookup
var credentialHeaders = []string{"Authorization", "Proxy-Authorization", "Cookie"}

// CredentialRedirectPolicy is the redirect rule of checks that send
// credentials and follow redirects (Prometheus, WebSocket; spec
// 2026-11-15-02). Use CheckRedirect as http.Client.CheckRedirect. It:
//
//   - caps the chain at MaxVia previous requests (DefaultMaxRedirects when 0);
//   - applies ApplyRedirectHop (no Referer on a cross-origin hop);
//   - on a cross-origin hop, deletes Authorization, Proxy-Authorization,
//     Cookie and every configured header, and records their names;
//   - stops following (http.ErrUseLastResponse) when the cross-origin hop
//     would replay a request body.
//
// A policy belongs to one execution: CheckRedirect runs synchronously on the
// requesting goroutine, so it is not safe for concurrent requests.
type CredentialRedirectPolicy struct {
	// MaxVia caps the chain; 0 means DefaultMaxRedirects.
	MaxVia int
	// Headers are the names of the headers the check configured.
	Headers []string

	dropped      []string
	bodyWithheld bool
}

// NewCredentialRedirectPolicy returns a policy withholding the given
// configured header names (plus the credential headers) from a cross-origin
// hop.
func NewCredentialRedirectPolicy(headers []string) *CredentialRedirectPolicy {
	return &CredentialRedirectPolicy{Headers: headers}
}

// HeaderNames returns the keys of a configured header map.
func HeaderNames(headers map[string]string) []string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}

	return names
}

// CheckRedirect is the http.Client.CheckRedirect callback.
func (p *CredentialRedirectPolicy) CheckRedirect(req *http.Request, via []*http.Request) error {
	maxVia := p.MaxVia
	if maxVia <= 0 {
		maxVia = DefaultMaxRedirects
	}

	if len(via) >= maxVia {
		return fmt.Errorf("%w: stopped after %d redirects", ErrTooManyRedirects, len(via))
	}

	hop := ApplyRedirectHop(via[0].URL, req)
	if !hop.CrossOrigin {
		return nil
	}

	original := via[0].Header

	for _, name := range slices.Concat(credentialHeaders, p.Headers) {
		canonical := http.CanonicalHeaderKey(name)
		// net/http may already have stripped Authorization or Cookie on a
		// host change: report what the check sent, not what was left.
		present := len(original.Values(canonical)) > 0 || len(req.Header.Values(canonical)) > 0

		req.Header.Del(canonical)

		if present && !slices.Contains(p.dropped, canonical) {
			p.dropped = append(p.dropped, canonical)
		}
	}

	slices.Sort(p.dropped)

	if hop.WithholdBody {
		p.bodyWithheld = true

		return http.ErrUseLastResponse
	}

	return nil
}

// Dropped returns the sorted names withheld so far.
func (p *CredentialRedirectPolicy) Dropped() []string {
	return slices.Clone(p.dropped)
}

// BodyWithheld reports whether a cross-origin hop was refused because it
// would have replayed the request body.
func (p *CredentialRedirectPolicy) BodyWithheld() bool {
	return p.bodyWithheld
}

// ReportDropped records the withheld names in a result output under
// OutputKeyRedirectDroppedSecretHeaders, when there are any.
func (p *CredentialRedirectPolicy) ReportDropped(output map[string]any) {
	if output == nil || len(p.dropped) == 0 {
		return
	}

	output[OutputKeyRedirectDroppedSecretHeaders] = p.Dropped()
}
