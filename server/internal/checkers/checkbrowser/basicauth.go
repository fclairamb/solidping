package checkbrowser

import (
	"context"
	"errors"
	"fmt"
	"net"
	neturl "net/url"
	"strings"
	"sync"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/chromedp"
)

// maxBasicAuthAttempts is how many times one execution answers a challenge
// from the credentials' origin. Two lets a server that challenges once more
// after a first success (a second realm, a session renewal) through; a third
// challenge means the credentials are wrong, and canceling it ends the
// navigation instead of looping on a 401.
const maxBasicAuthAttempts = 2

// ErrBasicAuthRejected is the credentials armed by BasicAuth refused by their
// origin: every allowed attempt answered with another challenge.
var ErrBasicAuthRejected = errors.New("basic auth rejected")

// errBasicAuthOrigin is an `origin` option that is not an http(s) origin.
var errBasicAuthOrigin = errors.New("basicAuth: origin must be an http or https origin, like https://acme.com")

// basicAuth is the HTTP Basic credentials a script handed to the page, and
// how they have been used so far.
//
// The credentials are scoped to ONE origin: the one given as an option, or
// else the origin of the next Navigate. A challenge from any other origin
// (a CDN, a third-party script, an iframe) is canceled, so the password never
// leaves the site it belongs to. That is the whole reason this is a Fetch
// auth handler and not an extra Authorization header, which Chrome would send
// to every origin the page loads.
type basicAuth struct {
	mu sync.Mutex

	// set is whether BasicAuth was called at all.
	set      bool
	username string
	password string
	// origin is the scheme://host:port the credentials are answered for, ""
	// until it is bound by an option or by the next Navigate.
	origin string
	// attempts counts the challenges answered for origin.
	attempts int
	// rejections counts the challenges canceled because attempts ran out.
	// Navigate compares it before and after to tell "the credentials were
	// refused during this navigation".
	rejections int

	// listening is whether the Fetch listener is registered on the target,
	// enabled whether Fetch.enable succeeded. Kept apart so a failed enable
	// retried by a second BasicAuth call does not register a second listener.
	listening bool
	enabled   bool
}

// BasicAuth arms HTTP Basic credentials for the page, to be called before the
// Navigate they are for. origin, when not empty, is the only origin the
// credentials are answered for; otherwise it is the origin of the next
// Navigate. Calling it again replaces the credentials and resets the attempt
// count.
//
// No error it returns carries the username or the password.
func (s *Session) BasicAuth(ctx context.Context, username, password, origin string) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()

	if closed {
		return errSessionClosed
	}

	bound := ""

	if origin != "" {
		normalized, ok := originOf(origin)
		if !ok {
			return errBasicAuthOrigin
		}

		bound = normalized
	}

	s.auth.mu.Lock()
	s.auth.set = true
	s.auth.username = username
	s.auth.password = password
	s.auth.origin = bound
	s.auth.attempts = 0
	s.auth.rejections = 0
	needsListener := !s.auth.listening
	s.auth.listening = true
	enabled := s.auth.enabled
	s.auth.mu.Unlock()

	if needsListener {
		//nolint:contextcheck // the listener belongs to the session's browser context, not the caller's
		chromedp.ListenTarget(s.browserCtx, s.onFetchEvent)
	}

	if enabled {
		return nil
	}

	// Pattern "*" is what makes Chrome route the page's requests through the
	// Fetch domain at all, and so raise authRequired. Every requestPaused is
	// continued untouched (onFetchEvent), so the request Chrome sends — and the
	// egress watcher's Network events about it — are exactly what they were
	// without this.
	err := s.run(ctx, fetch.Enable().
		WithHandleAuthRequests(true).
		WithPatterns([]*fetch.RequestPattern{{URLPattern: "*"}}))
	if err != nil {
		return fmt.Errorf("basicAuth: failed to enable authentication handling: %w", err)
	}

	s.auth.mu.Lock()
	s.auth.enabled = true
	s.auth.mu.Unlock()

	return nil
}

// bindBasicAuthOrigin gives credentials armed without an origin the origin of
// the navigation about to happen, and returns the bound origin and the
// rejection count to compare against once the navigation is done.
func (s *Session) bindBasicAuthOrigin(rawURL string) (string, int) {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()

	if !s.auth.set {
		return "", 0
	}

	if s.auth.origin == "" {
		if origin, ok := originOf(rawURL); ok {
			s.auth.origin = origin
		}
	}

	return s.auth.origin, s.auth.rejections
}

// basicAuthRejectedSince reports whether a challenge from the credentials'
// origin was canceled after the rejection count read before a navigation.
func (s *Session) basicAuthRejectedSince(before int) bool {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()

	return s.auth.set && s.auth.rejections > before
}

// onFetchEvent is the Fetch listener. It runs on chromedp's event loop, which
// must never block on a CDP round-trip, so every reply goes out on its own
// goroutine.
func (s *Session) onFetchEvent(ev any) {
	switch event := ev.(type) {
	case *fetch.EventRequestPaused:
		go s.fetchReply(fetch.ContinueRequest(event.RequestID))
	case *fetch.EventAuthRequired:
		go s.fetchReply(fetch.ContinueWithAuth(event.RequestID, s.answerChallenge(event)))
	}
}

// answerChallenge decides what one auth challenge gets: the credentials, only
// for a server challenge from their own origin and only maxBasicAuthAttempts
// times; a cancel for everything else, proxies included.
func (s *Session) answerChallenge(event *fetch.EventAuthRequired) *fetch.AuthChallengeResponse {
	cancel := &fetch.AuthChallengeResponse{Response: fetch.AuthChallengeResponseResponseCancelAuth}

	challenge := event.AuthChallenge
	if challenge == nil || challenge.Source == fetch.AuthChallengeSourceProxy {
		return cancel
	}

	origin, ok := originOf(challenge.Origin)
	if !ok && event.Request != nil {
		origin, ok = originOf(event.Request.URL)
	}

	if !ok {
		return cancel
	}

	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()

	if !s.auth.set || s.auth.origin == "" || origin != s.auth.origin {
		return cancel
	}

	if s.auth.attempts >= maxBasicAuthAttempts {
		s.auth.rejections++

		return cancel
	}

	s.auth.attempts++

	return &fetch.AuthChallengeResponse{
		Response: fetch.AuthChallengeResponseResponseProvideCredentials,
		Username: s.auth.username,
		Password: s.auth.password,
	}
}

// fetchReply sends one Fetch reply on the page's target. A failure means the
// page is gone, and so is the request it was about: nothing to report.
func (s *Session) fetchReply(action chromedp.Action) {
	c := chromedp.FromContext(s.browserCtx)
	if c == nil || c.Target == nil {
		return
	}

	_ = action.Do(cdp.WithExecutor(s.browserCtx, c.Target))
}

// basicAuthRejectedError is the credentials refused by origin. It names the
// origin and deliberately nothing else: no username, no password.
func basicAuthRejectedError(origin string) error {
	display := strings.TrimSuffix(origin, ":443")
	if strings.HasPrefix(origin, "http://") {
		display = strings.TrimSuffix(origin, ":80")
	}

	return fmt.Errorf("%w by %s", ErrBasicAuthRejected, display)
}

// originOf normalizes an http(s) URL or origin to scheme://host:port, the
// default port made explicit, so "https://acme.com" and
// "https://ACME.com:443/x" are the same origin.
func originOf(raw string) (string, bool) {
	parsed, err := neturl.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return "", false
	}

	scheme := strings.ToLower(parsed.Scheme)

	port := parsed.Port()

	switch scheme {
	case "http":
		if port == "" {
			port = "80"
		}
	case "https":
		if port == "" {
			port = "443"
		}
	default:
		return "", false
	}

	return scheme + "://" + net.JoinHostPort(strings.ToLower(parsed.Hostname()), port), true
}
