package notifications

import (
	"context"
	"errors"
	"fmt"
	neturl "net/url"
	"strings"

	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// ErrSenderURLInvalid is the sentinel every ValidateSenderURL rejection
// wraps: a malformed URL, one carrying userinfo, a non-http(s) scheme, or —
// when guard is enforcing — a host that resolves to a loopback/link-local/
// private address. errors.Is(err, ErrSenderURLInvalid) is true for every
// rejection regardless of cause; errors.As can still recover the underlying
// *egress.DeniedError when the cause was the egress policy, e.g. to read
// which host was refused.
var ErrSenderURLInvalid = errors.New("url must be a public http(s) endpoint")

// ValidateSenderURL checks that rawURL is a syntactically valid http(s) URL
// with a host and no embedded credentials, and — when guard is enforcing —
// that the host is not a loopback/link-local/private/CGNAT/metadata address
// (spec 2026-09-25-20, reusing the egress package from spec 2026-09-25-19).
//
// guard may be nil, meaning "no policy": every syntactically valid http(s)
// URL passes, matching a nil *egress.Guard's own "allow everything" contract.
//
// Called at integration create/update (handlers/integrations/service.go) so a
// bad or non-public URL never reaches storage, and defensively by each sender
// immediately before it builds a request: a row can predate this validator,
// or be saved while the policy allowed private targets and later have the
// policy tightened under it. The guarded HTTP transport each sender also
// dials through (see httpclientpool.NewGuardedClient) is the real defense
// against DNS rebinding between validation and the actual connection — this
// function only exists to fail fast and legibly before any network attempt,
// and to give integration CRUD a clean 400 instead of a delivery-time
// failure.
func ValidateSenderURL(ctx context.Context, guard *egress.Guard, rawURL string) error {
	parsed, err := neturl.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSenderURLInvalid, err)
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("%w: scheme must be http or https, got %q", ErrSenderURLInvalid, parsed.Scheme)
	}

	if parsed.User != nil {
		return fmt.Errorf("%w: must not contain userinfo", ErrSenderURLInvalid)
	}

	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("%w: missing host", ErrSenderURLInvalid)
	}

	// ParseURLHost reads the host the way a browser does, catching the
	// numeric forms net.ParseIP misses (2130706433, 0x7f000001, 127.1, …) so
	// none of them slip through as an apparent, unresolvable "domain name".
	ip, err := egress.ParseURLHost(host)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSenderURLInvalid, err)
	}

	if ip != nil {
		if denyErr := guard.CheckHostIP(host, ip); denyErr != nil {
			return fmt.Errorf("%w: %w", ErrSenderURLInvalid, denyErr)
		}

		return nil
	}

	// A domain name. When the guard isn't enforcing (self-hosted/test-mode
	// default, or an operator explicitly allowing private targets), nothing
	// downstream will reject it either — skip the DNS round trip entirely so
	// saving a webhook URL never pays for a lookup that decides nothing.
	if !guard.Enforcing() {
		return nil
	}

	if _, err := guard.Resolve(ctx, host); err != nil {
		var denied *egress.DeniedError
		if errors.As(err, &denied) {
			return fmt.Errorf("%w: %w", ErrSenderURLInvalid, denied)
		}

		// A non-denial resolution failure (DNS hiccup, a name that doesn't
		// exist yet, no network in a sandboxed run) is not this validator's
		// call to make: the sender's own request will hit and report the
		// identical failure moments later. Failing validation closed here
		// would also block saving a URL whose host is briefly unresolvable.
		return nil
	}

	return nil
}

// egressGuardFrom returns the process's outbound-connection policy for
// notification sender URLs, carried on jctx.Services.EgressGuard, or nil
// ("no policy") when jctx or jctx.Services is nil — the construction every
// sender's own unit tests use (a bare *jobdef.JobContext{Logger: ...}).
func egressGuardFrom(jctx *jobdef.JobContext) *egress.Guard {
	if jctx == nil || jctx.Services == nil {
		return nil
	}

	return jctx.Services.EgressGuard
}
