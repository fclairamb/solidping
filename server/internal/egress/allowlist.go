package egress

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
)

// ErrHostNotAllowed is the sentinel a HostAllowlist refusal matches with
// errors.Is.
var ErrHostNotAllowed = errors.New("host not allowed while secrets are loaded")

// HostNotAllowedError is a connection the execution's HostAllowlist refused.
// Target is the host, or the API, that was refused.
type HostNotAllowedError struct {
	Target string
}

func (e *HostNotAllowedError) Error() string {
	return ErrHostNotAllowed.Error() + ": " + e.Target
}

// Is makes errors.Is(err, ErrHostNotAllowed) match.
func (e *HostNotAllowedError) Is(target error) bool {
	return target == ErrHostNotAllowed
}

// HostAllowlist holds one execution to a fixed set of hosts. The AI authoring
// loops put one on the context of every run that gets real secret values, so
// a script built at runtime (concatenated URLs, hosts read from a response,
// dynamic env keys) still cannot send those values to a host nobody vetted.
//
// It is checked on the host NAME, before any resolution, so a refused name
// does not even leave as a DNS query. A nil *HostAllowlist allows everything.
type HostAllowlist struct {
	hosts map[string]struct{}

	mu      sync.Mutex
	refused *HostNotAllowedError
}

// NewHostAllowlist builds an allowlist of hosts (names or IP literals, case
// and a trailing dot ignored). An empty list allows nothing.
func NewHostAllowlist(hosts []string) *HostAllowlist {
	list := &HostAllowlist{hosts: make(map[string]struct{}, len(hosts))}

	for _, host := range hosts {
		if normalized := normalizeHost(host); normalized != "" {
			list.hosts[normalized] = struct{}{}
		}
	}

	return list
}

// Check refuses a host (or host:port) outside the list, recording the first
// refusal. Nil for a nil list.
func (a *HostAllowlist) Check(host string) error {
	if a == nil {
		return nil
	}

	if split, _, err := net.SplitHostPort(host); err == nil {
		host = split
	}

	normalized := normalizeHost(host)
	if _, ok := a.hosts[normalized]; ok && normalized != "" {
		return nil
	}

	return a.refuse(host)
}

// Refuse records and returns a refusal of something the list cannot vouch
// for (a browser, a sub-check), named by what.
func (a *HostAllowlist) Refuse(what string) error {
	if a == nil {
		return nil
	}

	return a.refuse(what)
}

func (a *HostAllowlist) refuse(target string) error {
	err := &HostNotAllowedError{Target: target}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.refused == nil {
		a.refused = err
	}

	return err
}

// Refused returns the first refusal, or nil.
func (a *HostAllowlist) Refused() *HostNotAllowedError {
	if a == nil {
		return nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	return a.refused
}

// RoundTripper wraps next so every request, redirects included, is checked
// against the list before it is sent. It returns next for a nil list.
func (a *HostAllowlist) RoundTripper(next http.RoundTripper) http.RoundTripper {
	if a == nil {
		return next
	}

	if next == nil {
		next = http.DefaultTransport
	}

	return allowlistTransport{list: a, next: next}
}

type allowlistTransport struct {
	list *HostAllowlist
	next http.RoundTripper
}

func (t allowlistTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.list.Check(req.URL.Hostname()); err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}

		return nil, err
	}

	return t.next.RoundTrip(req)
}

func normalizeHost(host string) string {
	host = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(host), "["), "]")

	return strings.TrimSuffix(strings.ToLower(host), ".")
}

type allowlistCtxKey struct{}

// WithHostAllowlist returns a context whose execution may only connect to
// the hosts of list.
func WithHostAllowlist(ctx context.Context, list *HostAllowlist) context.Context {
	return context.WithValue(ctx, allowlistCtxKey{}, list)
}

// HostAllowlistFrom returns the allowlist carried by ctx, or nil (allow
// everything).
func HostAllowlistFrom(ctx context.Context) *HostAllowlist {
	if ctx == nil {
		return nil
	}

	list, _ := ctx.Value(allowlistCtxKey{}).(*HostAllowlist)

	return list
}
