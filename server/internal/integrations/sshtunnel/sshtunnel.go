// Package sshtunnel resolves an SSH *check* into a live port-forwarding dialer.
//
// The model is check-depends-on-check: a tunnel-capable check carries a
// `tunnelCheckUid` in its config referencing an SSH check in the same org. That
// SSH check is the single home for the bastion's credentials (already encrypted
// at rest via checkssh's SecretFields), the bastion gets monitored for free, and
// the dependency edge is the future hook for parent/child alert suppression —
// which is why there is no separate "tunnel" resource type or credential store.
//
// Resolution happens server-side at EXECUTE time through a package-level
// resolver (the checkkubernetes.ClientsetResolverFunc / checkfreeboxline
// .ConnectionResolverFunc pattern), wired once in app/server.go. Deliberately
// not via the dispatched job config: it keeps the checkers free of DB and
// credential concerns, and it does not depend on the job-config merge path. When
// deported agents grow tunnel support, private-region jobs will need the tunnel
// config sealed into the dispatched job instead — hence Resolver is an
// interface-shaped func that WithResolver can swap per execution context.
package sshtunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkssh"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/sshauth"
)

const (
	defaultSSHPort    = 22
	defaultSSHTimeout = 10 * time.Second
)

// Sentinel causes behind an *Error. They are the machine-readable half of
// decision 10's failure classification: every one of them means "the tunnel
// could not be established", never "the target behind it is down".
var (
	// ErrNotConfigured means no resolver was wired — a startup/test wiring bug.
	ErrNotConfigured = errors.New("ssh tunnel: resolver is not configured")
	// ErrNotAvailableOnAgent means a deported agent received a tunneled job with
	// no unsealed tunnel snapshot for it (version skew, or a race with the
	// dispatch-time drop). Distinct from ErrNotConfigured's wiring-bug flavor:
	// the fix is allocating the SSH check to this agent's region (spec
	// 2026-07-18-07, decision 7).
	ErrNotAvailableOnAgent = errors.New(
		"ssh tunnel: not available on this agent (the SSH check must be allocated to this agent's region)")
	// ErrCheckNotFound means the referenced check does not exist in the org
	// (or was deleted). Cross-org references land here too: the lookup is
	// org-scoped, so another org's check is simply invisible.
	ErrCheckNotFound = errors.New("ssh tunnel: referenced check not found in this organization")
	// ErrNotSSHCheck means the referenced check exists but is not type `ssh`.
	ErrNotSSHCheck = errors.New("ssh tunnel: referenced check is not an ssh check")
	// ErrNoFingerprint means the SSH check has no `expected_fingerprint`.
	// Host-key verification is mandatory for tunnel use: the tunnel carries the
	// probe's traffic, so an unverified bastion is a silent MITM. (A plain SSH
	// check without a fingerprint keeps working as a check — the requirement
	// applies only when it is referenced as a tunnel.)
	ErrNoFingerprint = errors.New(
		"ssh tunnel: the ssh check must set expected_fingerprint to be used as a tunnel",
	)
	// ErrChained means the SSH check itself carries a `tunnelCheckUid`. v1
	// forbids chaining, which kills cycles trivially; multi-hop bastions are a
	// possible follow-up.
	ErrChained = errors.New("ssh tunnel: the ssh check is itself tunneled (chaining is not supported)")
	// ErrNoCredentials means the SSH check has no usable auth material.
	ErrNoCredentials = errors.New("ssh tunnel: the ssh check needs a username and a password or private_key")
	// ErrSecretsUnavailable means the check has an encrypted private config but
	// the credentials service cannot decrypt it.
	ErrSecretsUnavailable = errors.New("ssh tunnel: cannot decrypt the ssh check credentials")
	// ErrHostKeyMismatch means the bastion presented a key that is not the
	// expected fingerprint.
	ErrHostKeyMismatch = errors.New("ssh tunnel: host key does not match expected_fingerprint")
	// ErrForwardRejected means the bastion refused to open the forward channel
	// itself (administratively prohibited, resource shortage) — as opposed to
	// the forward succeeding and the TARGET refusing the connection, which is a
	// plain target failure and must NOT be classified as a tunnel failure.
	ErrForwardRejected = errors.New("ssh tunnel: the bastion rejected the port forward")
)

// Error is the typed error every resolution/dial failure is wrapped in, so the
// worker can classify the execution as a tunnel failure (distinct output +
// marker) rather than as a failure of the target behind the tunnel.
type Error struct {
	// Op is the stage that failed ("load", "credentials", "dial", "handshake",
	// "forward"), included in the message so an operator can tell a broken
	// bastion from bad credentials at a glance.
	Op string
	// Err is the underlying cause — one of the sentinels above, or a transport
	// error from the network/SSH layer.
	Err error
}

// Error renders as "<op>: <cause>" — the worker prefixes the whole thing with
// "tunnel failed: " when it writes the result output, so the op reads as the
// stage that broke ("tunnel failed: handshake: ssh: unable to authenticate…").
func (e *Error) Error() string {
	return fmt.Sprintf("%s: %v", e.Op, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

func wrapErr(op string, err error) *Error {
	return &Error{Op: op, Err: err}
}

// IsTunnelError reports whether err came from tunnel establishment or from the
// tunnel transport itself.
func IsTunnelError(err error) bool {
	var tunnelErr *Error

	return errors.As(err, &tunnelErr)
}

// CheckLoader is the narrow slice of the DB service the resolver needs. Taking
// an interface rather than db.Service keeps this package unit-testable without
// a database and documents that resolution reads exactly one row.
type CheckLoader interface {
	// GetCheck returns a non-deleted check by UID within an org.
	GetCheck(ctx context.Context, orgUID, checkUID string) (*models.Check, error)
}

// Decryptor is the narrow slice of the credentials service the resolver needs.
type Decryptor interface {
	Enabled() bool
	DecryptForOrg(ctx context.Context, orgUID string, envelope string) (map[string]any, error)
}

// Resolver turns (org, ssh-check uid) into a live dialer plus the closer that
// tears the SSH client down. Callers MUST close the returned io.Closer.
//
// Each execution resolves and dials its own fresh session by design (v1): the
// dependency is config-level, not runtime-level — nothing is reused from the SSH
// check's own scheduled runs, so disabling or pausing the SSH check does not
// stop dependents, and the dependent's regions are independent of the SSH
// check's. Pooling is a deliberate follow-up.
type Resolver func(ctx context.Context, orgUID, tunnelCheckUID string) (*Dialer, io.Closer, error)

// ResolverFunc is the active resolver. Production wires it once at startup;
// tests should prefer WithResolver(ctx, …) so parallel tests don't race over a
// single global.
//
//nolint:gochecknoglobals // Mirrors checkkubernetes.ClientsetResolverFunc.
var ResolverFunc Resolver

type resolverCtxKeyType struct{}

//nolint:gochecknoglobals // sentinel value for a context key
var resolverCtxKey = resolverCtxKeyType{}

// WithResolver returns a context carrying a resolver that takes precedence over
// the package-level ResolverFunc. This is the seam a different execution
// context (a deported agent that receives sealed tunnel config with its job
// instead of reading the DB) swaps out.
func WithResolver(ctx context.Context, resolver Resolver) context.Context {
	return context.WithValue(ctx, resolverCtxKey, resolver)
}

// ResolverFrom returns the per-context resolver if set, otherwise the
// package-level ResolverFunc (nil when neither is wired).
func ResolverFrom(ctx context.Context) Resolver {
	if resolver, ok := ctx.Value(resolverCtxKey).(Resolver); ok && resolver != nil {
		return resolver
	}

	return ResolverFunc
}

// CarryResolver copies a per-context resolver override from src onto dst.
//
// The worker deliberately detaches its execution context from the job context
// (a claimed check must finish even while the server shuts down), and detaching
// drops every context value with it. Without carrying it across that boundary,
// a resolver injected per-execution — the seam a deported agent uses to resolve
// tunnels from sealed job config instead of the DB, and the seam tests use to
// avoid racing over the global — would silently never be consulted, and the
// global would answer instead. Returns dst unchanged when src has no override.
func CarryResolver(src, dst context.Context) context.Context {
	if resolver, ok := src.Value(resolverCtxKey).(Resolver); ok && resolver != nil {
		return WithResolver(dst, resolver)
	}

	return dst
}

// Resolve establishes a tunnel through the given SSH check using whichever
// resolver is active for ctx. It is the single entry point callers use.
func Resolve(ctx context.Context, orgUID, tunnelCheckUID string) (*Dialer, io.Closer, error) {
	resolver := ResolverFrom(ctx)
	if resolver == nil {
		return nil, nil, wrapErr("resolve", ErrNotConfigured)
	}

	return resolver(ctx, orgUID, tunnelCheckUID)
}

// NewResolver builds the production resolver: load the referenced check, assert
// every tunnel-eligibility rule, decrypt its credentials, and dial the bastion.
func NewResolver(loader CheckLoader, creds Decryptor) Resolver {
	return func(ctx context.Context, orgUID, tunnelCheckUID string) (*Dialer, io.Closer, error) {
		cfg, err := LoadConfig(ctx, loader, creds, orgUID, tunnelCheckUID)
		if err != nil {
			return nil, nil, err
		}

		return Dial(ctx, cfg)
	}
}

// LoadConfig loads the referenced SSH check and validates every rule that makes
// it usable as a tunnel, returning its decrypted config. Split out of the dial
// so the rules can be unit-tested without a network.
func LoadConfig(
	ctx context.Context, loader CheckLoader, creds Decryptor, orgUID, tunnelCheckUID string,
) (*checkssh.SSHConfig, error) {
	check, err := loader.GetCheck(ctx, orgUID, tunnelCheckUID)
	if err != nil || check == nil {
		return nil, wrapErr("load", fmt.Errorf("%w: %s", ErrCheckNotFound, tunnelCheckUID))
	}

	if check.Type != string(checkerdef.CheckTypeSSH) {
		return nil, wrapErr("load", fmt.Errorf("%w: %s is %q", ErrNotSSHCheck, tunnelCheckUID, check.Type))
	}

	effective, err := effectiveConfig(ctx, creds, check)
	if err != nil {
		return nil, err
	}

	if _, chained := checkerdef.TunnelCheckUIDFrom(effective); chained {
		return nil, wrapErr("load", fmt.Errorf("%w: %s", ErrChained, tunnelCheckUID))
	}

	cfg := &checkssh.SSHConfig{}
	if err := cfg.FromMap(effective); err != nil {
		return nil, wrapErr("load", err)
	}

	if cfg.ExpectedFingerprint == "" {
		return nil, wrapErr("load", fmt.Errorf("%w: %s", ErrNoFingerprint, tunnelCheckUID))
	}

	if cfg.Username == "" || (cfg.Password == "" && cfg.PrivateKey == "") {
		return nil, wrapErr("credentials", fmt.Errorf("%w: %s", ErrNoCredentials, tunnelCheckUID))
	}

	return cfg, nil
}

// effectiveConfig merges the check's public config with its decrypted private
// side. A v3 plaintext envelope (credentials.IsPlaintextEnvelope) — the
// no-master-key structural-separation fallback that keeps secrets out of the
// public config column — opens without any key and is merged in up front, with
// no dependency on creds at all: creds can legitimately be nil here. Only an
// envelope that actually requires a key (AES-GCM v1, region-sealed v2) is
// gated on creds.Enabled(). Mirrors checkjobsvc.MergeJobSecrets.
func effectiveConfig(ctx context.Context, creds Decryptor, check *models.Check) (map[string]any, error) {
	public := map[string]any(check.Config)

	if check.ConfigPrivate == nil || *check.ConfigPrivate == "" {
		return public, nil
	}

	if credentials.IsPlaintextEnvelope(*check.ConfigPrivate) {
		private, err := credentials.OpenPlaintext(*check.ConfigPrivate)
		if err != nil {
			return nil, wrapErr("credentials", fmt.Errorf("%w: %w", ErrSecretsUnavailable, err))
		}

		return credentials.MergeConfig(public, private), nil
	}

	if creds == nil || !creds.Enabled() {
		return nil, wrapErr("credentials", fmt.Errorf("%w: %s (encryption disabled)", ErrSecretsUnavailable, check.UID))
	}

	private, err := creds.DecryptForOrg(ctx, check.OrganizationUID, *check.ConfigPrivate)
	if err != nil {
		return nil, wrapErr("credentials", fmt.Errorf("%w: %w", ErrSecretsUnavailable, err))
	}

	return credentials.MergeConfig(public, private), nil
}

// Dial opens the SSH connection to the bastion described by cfg and returns a
// dialer forwarding through it. Host-key verification is strict against
// cfg.ExpectedFingerprint — ssh.InsecureIgnoreHostKey is never used here.
func Dial(ctx context.Context, cfg *checkssh.SSHConfig) (*Dialer, io.Closer, error) {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultSSHTimeout
	}

	port := cfg.Port
	if port == 0 {
		port = defaultSSHPort
	}

	authMethods, err := authMethodsFor(cfg)
	if err != nil {
		return nil, nil, err
	}

	// The BASTION's own hostname is resolved locally — it is by definition
	// reachable from the worker. Only the target behind it gets remote-side
	// resolution (see Dialer.DialContext).
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))

	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return nil, nil, wrapErr("dial", err)
	}

	// The SSH handshake below is not context-aware; a connection deadline
	// bounds it, and is cleared once the client is up so it can't kill
	// long-lived forwards.
	if deadlineErr := conn.SetDeadline(time.Now().Add(timeout)); deadlineErr != nil {
		_ = conn.Close()

		return nil, nil, wrapErr("dial", deadlineErr)
	}

	clientConfig := &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            authMethods,
		HostKeyCallback: fixedFingerprintCallback(cfg.ExpectedFingerprint),
		Timeout:         timeout,
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientConfig)
	if err != nil {
		_ = conn.Close()

		// Covers auth failure and host-key mismatch alike: the callback's
		// ErrHostKeyMismatch is wrapped by x/crypto and stays matchable through
		// errors.Is on the returned *Error.
		return nil, nil, wrapErr("handshake", err)
	}

	if deadlineErr := conn.SetDeadline(time.Time{}); deadlineErr != nil {
		_ = sshConn.Close()

		return nil, nil, wrapErr("handshake", deadlineErr)
	}

	client := ssh.NewClient(sshConn, chans, reqs)

	return &Dialer{client: client}, client, nil
}

func authMethodsFor(cfg *checkssh.SSHConfig) ([]ssh.AuthMethod, error) {
	if cfg.PrivateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(cfg.PrivateKey))
		if err != nil {
			return nil, wrapErr("credentials", fmt.Errorf("%w: %w", ErrNoCredentials, err))
		}

		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}

	if cfg.Password != "" {
		return sshauth.PasswordMethods(cfg.Password), nil
	}

	return nil, wrapErr("credentials", ErrNoCredentials)
}

// fixedFingerprintCallback verifies the presented host key against the
// `SHA256:<base64>` fingerprint stored on the SSH check, using checkssh's own
// rendering so the two can never disagree on format.
func fixedFingerprintCallback(expected string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		got := checkssh.Fingerprint(key)
		if got != expected {
			return fmt.Errorf("%w: got %s, expected %s", ErrHostKeyMismatch, got, expected)
		}

		return nil
	}
}

// Dialer forwards connections through an established SSH client, satisfying
// checkerdef.ContextDialer.
type Dialer struct {
	client *ssh.Client

	mu sync.Mutex
	// tunnelFailure records the first failure attributable to the TUNNEL rather
	// than to the target — see DialContext for how the two are told apart.
	tunnelFailure error
}

// DialContext opens a forwarded connection to addr through the bastion.
//
// addr is passed through as the raw `host:port` the check configured: the
// direct-tcpip request carries the hostname and the BASTION resolves it. That
// is the point — a private hostname the worker's resolver could never see (nor
// should try to) resolves correctly on the far side.
func (d *Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := d.client.DialContext(ctx, network, addr)
	if err != nil {
		d.record(addr, err)

		return nil, err
	}

	// SSH channels reject SetDeadline; wrap so probes that bound their reads
	// (checktcp does) behave as they would on a direct socket.
	return newDeadlineConn(conn), nil
}

// record classifies a forward failure. A rejection whose reason is
// ConnectionFailed means the bastion tried and the TARGET refused — that is the
// target being down, exactly what the check is meant to detect, so it is left
// alone. Any other rejection reason (administratively prohibited, resource
// shortage, unknown channel type) means the bastion itself would not do the
// forward, which is a tunnel failure. Transport errors (the SSH connection
// died mid-check) are tunnel failures too.
func (d *Dialer) record(addr string, err error) {
	var openErr *ssh.OpenChannelError
	if errors.As(err, &openErr) && openErr.Reason == ssh.ConnectionFailed {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.tunnelFailure == nil {
		d.tunnelFailure = wrapErr("forward", fmt.Errorf("%w: %s: %w", ErrForwardRejected, addr, err))
	}
}

// TunnelFailure returns the first tunnel-attributable failure observed while
// forwarding, or nil. The worker consults it after execution so a bastion that
// refuses to forward is reported as a tunnel failure rather than as the target
// being down.
func (d *Dialer) TunnelFailure() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.tunnelFailure
}
