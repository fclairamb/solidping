package checkerdef

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fclairamb/solidping/server/internal/egress"
)

// This file is the ONE place that decides whether a check may open a Unix
// socket (spec 2026-11-24-01). SolidPing often runs as root, and one request
// on /var/run/docker.sock is root on the host, so no checker dials a socket
// itself: each one calls DialLocalSocket (or SocketHTTPTransport), and the
// save path applies the very same path rules through ValidateLocalSocketPath
// and LocalSocketAllowed.

// LocalSocketSetting is the configuration key of the socket allowlist, named
// in every refusal so an operator knows which knob to turn.
const LocalSocketSetting = "checkers.unix_socket_paths"

// Errors of the local-socket gates. Each refusal wraps one of them.
var (
	// ErrLocalSocketPath is a socket path that is not a plain, absolute,
	// clean filesystem path.
	ErrLocalSocketPath = errors.New("invalid unix socket path")
	// ErrLocalSocketNotAllowed is a socket path outside this process's
	// allowlist (or any path, when the allowlist is empty).
	ErrLocalSocketNotAllowed = errors.New("unix socket path is not allowed on this server")
	// ErrLocalSocketTunneled is a socket target on a tunneled check: a path
	// means nothing on the far side of an SSH bastion.
	ErrLocalSocketTunneled = errors.New("a unix socket cannot be reached through an SSH tunnel")
)

// LocalSocketTarget is implemented by a check config that can target a local
// Unix socket instead of a host and port. field is the config key holding the
// path (`socket`, or `unix_socket` for http), so a validation error lands on
// the right form field. A config returning a non-empty path is host-local:
// every host-local gate (IsHostLocal) reads this one method.
type LocalSocketTarget interface {
	LocalSocket() (field, path string)
}

// LocalSocketOf returns the socket target of cfg, ok=false when cfg does not
// implement LocalSocketTarget or holds no path.
func LocalSocketOf(cfg Config) (field, path string, ok bool) {
	target, isTarget := cfg.(LocalSocketTarget)
	if !isTarget || target == nil {
		return "", "", false
	}

	field, path = target.LocalSocket()

	return field, path, path != ""
}

// IsHostLocal reports whether a check of checkType with cfg reads something
// local to the host that executes it: the Docker daemon, or a Unix socket.
// Such a check must never run on a SaaS shared worker. cfg may be nil, in
// which case only the type-level rule (docker) applies.
func IsHostLocal(checkType CheckType, cfg Config) bool {
	if checkType == CheckTypeDocker {
		return true
	}

	if cfg == nil {
		return false
	}

	_, _, ok := LocalSocketOf(cfg)

	return ok
}

// ValidateLocalSocketPath is the syntax rule: an absolute, already-clean path
// with no NUL byte. Abstract sockets (`@name`) are out of scope.
func ValidateLocalSocketPath(path string) error {
	switch {
	case path == "":
		return fmt.Errorf("%w: the path is empty", ErrLocalSocketPath)
	case strings.ContainsRune(path, 0):
		return fmt.Errorf("%w: the path contains a NUL byte", ErrLocalSocketPath)
	case strings.HasPrefix(path, "@"):
		return fmt.Errorf("%w: abstract sockets (@name) are not supported", ErrLocalSocketPath)
	case !filepath.IsAbs(path) || !strings.HasPrefix(path, "/"):
		return fmt.Errorf("%w: %q must be an absolute path", ErrLocalSocketPath, path)
	case filepath.Clean(path) != path:
		return fmt.Errorf("%w: %q must be a clean path (no `..`, `.`, `//` or trailing `/`)", ErrLocalSocketPath, path)
	}

	return nil
}

// LocalSocketAllowed is the allowlist rule: path must equal an entry, or sit
// under an entry ending in "/". Both sides are compared after filepath.Clean,
// so `..` cannot climb out of a prefix. An empty allowlist refuses every path.
func LocalSocketAllowed(allowlist []string, path string) error {
	cleaned := filepath.Clean(path)

	for _, entry := range allowlist {
		entry = strings.TrimSpace(entry)
		if entry == "" || !strings.HasPrefix(entry, "/") {
			continue
		}

		if strings.HasSuffix(entry, "/") {
			prefix := filepath.Clean(entry)
			if prefix != "/" {
				prefix += "/"
			}

			if strings.HasPrefix(cleaned, prefix) && cleaned != prefix {
				return nil
			}

			continue
		}

		if filepath.Clean(entry) == cleaned {
			return nil
		}
	}

	if len(allowlist) == 0 {
		return fmt.Errorf("%w: unix sockets are off; an operator can list allowed paths in %s (SP_CHECKERS_UNIX_SOCKET_PATHS)",
			ErrLocalSocketNotAllowed, LocalSocketSetting)
	}

	return fmt.Errorf("%w: %s is not covered by %s", ErrLocalSocketNotAllowed, path, LocalSocketSetting)
}

type localSocketAllowlistCtxKey struct{}

// WithLocalSocketAllowlist returns a context carrying the socket allowlist of
// the process that executes the check. The worker sets it on every execution
// context; a context without one refuses every socket.
func WithLocalSocketAllowlist(ctx context.Context, allowlist []string) context.Context {
	return context.WithValue(ctx, localSocketAllowlistCtxKey{}, allowlist)
}

// LocalSocketAllowlistFrom returns the allowlist carried by ctx, nil if none.
func LocalSocketAllowlistFrom(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}

	allowlist, _ := ctx.Value(localSocketAllowlistCtxKey{}).([]string)

	return allowlist
}

// CheckLocalSocket runs every gate DialLocalSocket applies, in order, without
// dialing: tunnel, enforcing egress guard, path syntax, allowlist.
func CheckLocalSocket(ctx context.Context, path string) error {
	if TunnelDialerFrom(ctx) != nil {
		return ErrLocalSocketTunneled
	}

	if egress.FromContext(ctx).Enforcing() {
		denied := &egress.DeniedError{Host: "unix:" + path}
		egress.RecordDenial(ctx, denied)

		return denied
	}

	if err := ValidateLocalSocketPath(path); err != nil {
		return err
	}

	return LocalSocketAllowed(LocalSocketAllowlistFrom(ctx), path)
}

// LocalSocketDialError is a dial of an allowed socket that failed. Its message
// names the errno so a user can tell a missing file from a dead process from a
// permission problem.
type LocalSocketDialError struct {
	Path string
	Err  error
}

func (e *LocalSocketDialError) Error() string {
	switch {
	case errors.Is(e.Err, syscall.ENOENT):
		return fmt.Sprintf("unix socket %s: socket file missing (ENOENT)", e.Path)
	case errors.Is(e.Err, syscall.ECONNREFUSED):
		return fmt.Sprintf("unix socket %s: nobody is listening (ECONNREFUSED)", e.Path)
	case errors.Is(e.Err, syscall.EACCES), errors.Is(e.Err, syscall.EPERM):
		return fmt.Sprintf("unix socket %s: SolidPing lacks permission on the socket file (EACCES)", e.Path)
	default:
		return fmt.Sprintf("unix socket %s: %v", e.Path, e.Err)
	}
}

func (e *LocalSocketDialError) Unwrap() error { return e.Err }

// DialLocalSocket opens the Unix socket at path after every gate of
// CheckLocalSocket passed. timeout bounds the dial (0 = the context's own
// deadline only). A gate refusal never dials.
func DialLocalSocket(ctx context.Context, path string, timeout time.Duration) (net.Conn, error) {
	if err := CheckLocalSocket(ctx, path); err != nil {
		return nil, err
	}

	return dialUnix(ctx, path, timeout)
}

func dialUnix(ctx context.Context, path string, timeout time.Duration) (net.Conn, error) {
	dialer := net.Dialer{Timeout: timeout}

	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, &LocalSocketDialError{Path: path, Err: err}
	}

	return conn, nil
}

// LocalSocketDialer returns a ContextDialer that ignores the requested network
// and address and dials the socket at path through DialLocalSocket. The gates
// read gateCtx (the execution context, which carries the allowlist, the guard
// and the tunnel); the dial itself honors the context it is called with.
func LocalSocketDialer(gateCtx context.Context, path string, timeout time.Duration) ContextDialer {
	return DialerFunc(func(dialCtx context.Context, _, _ string) (net.Conn, error) {
		if err := CheckLocalSocket(gateCtx, path); err != nil {
			return nil, err
		}

		return dialUnix(dialCtx, path, timeout)
	})
}

// SocketHTTPTransport returns an HTTP transport whose every connection goes
// to the Unix socket at path, whatever host the request names, through the
// gates of DialLocalSocket evaluated on ctx.
func SocketHTTPTransport(ctx context.Context, path string) *http.Transport {
	transport := &http.Transport{
		DisableKeepAlives: true,
		Proxy:             nil,
	}
	transport.DialContext = LocalSocketDialer(ctx, path, 0).DialContext

	return transport
}
