package checkerdef_test

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// dialErr wraps an errno the way net.Dialer does, so errors.Is sees the syscall
// through the *net.OpError the classifier will meet in production.
func dialErr(errno syscall.Errno) error {
	return &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: &net.OpError{Op: "dial", Err: errno},
	}
}

// Static errors: the linter forbids inline errors.New in tests, and naming them
// also makes the table read as a catalog of failure shapes.
var (
	errRefusedText  = errors.New("dial tcp 10.0.0.1:443: connect: connection refused")
	errApplication  = errors.New("unexpected status code 500")
	errHandshakeEOF = errors.New("handshake: eof")
)

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestClassifyDialError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		timedOut bool
		want     string
	}{
		{"refused", dialErr(syscall.ECONNREFUSED), false, checkerdef.NetFailureConnectionRefused},
		{"network unreachable", dialErr(syscall.ENETUNREACH), false, checkerdef.NetFailureNetworkUnreachable},
		{"host unreachable", dialErr(syscall.EHOSTUNREACH), false, checkerdef.NetFailureHostUnreachable},
		{"errno timeout", dialErr(syscall.ETIMEDOUT), false, checkerdef.NetFailureConnectTimeout},
		{"context deadline", context.DeadlineExceeded, true, checkerdef.NetFailureConnectTimeout},
		{"net.Error timeout", timeoutError{}, false, checkerdef.NetFailureConnectTimeout},
		{
			"text fallback keeps classifying a stripped errno",
			errRefusedText,
			false,
			checkerdef.NetFailureConnectionRefused,
		},

		// The negatives. Each of these is a failure the trace has nothing to
		// say about, and each must classify as "" so no marker is minted.
		{"nil error, no timeout", nil, false, ""},
		{
			"dns not found",
			&net.DNSError{Err: "no such host", Name: "nope.acme.com", IsNotFound: true},
			false,
			"",
		},
		{
			"dns timeout is still not a reachability class",
			&net.DNSError{Err: "i/o timeout", Name: "slow.acme.com", IsTimeout: true},
			false,
			"",
		},
		{
			"certificate error",
			&x509.CertificateInvalidError{Reason: x509.Expired},
			false,
			"",
		},
		{"opaque application error", errApplication, false, ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := checkerdef.ClassifyDialError(test.err, test.timedOut); got != test.want {
				t.Fatalf("ClassifyDialError(%v, %v) = %q, want %q", test.err, test.timedOut, got, test.want)
			}
		})
	}
}

// TestClassifyDialErrorDNSTimeoutIsNotAConnectTimeout is the DNS negative with
// its positive control side by side: the SAME "i/o timeout" text classifies as
// a connect timeout when it is not a DNSError, so the empty answer above is the
// DNS check doing its job and not the classifier simply failing to match.
func TestClassifyDialErrorDNSTimeoutIsNotAConnectTimeout(t *testing.T) {
	t.Parallel()

	dnsTimeout := &net.DNSError{Err: "i/o timeout", Name: "slow.acme.com", IsTimeout: true}
	if got := checkerdef.ClassifyDialError(dnsTimeout, false); got != "" {
		t.Fatalf("a DNS timeout classified as %q, want no class", got)
	}

	// Positive control: identical text, not a DNSError.
	plain := fmt.Errorf("dial tcp 10.0.0.1:443: %w", timeoutError{})
	if got := checkerdef.ClassifyDialError(plain, false); got != checkerdef.NetFailureConnectTimeout {
		t.Fatalf("a plain dial timeout classified as %q, want %q", got, checkerdef.NetFailureConnectTimeout)
	}
}

func TestClassifyTLSHandshakeError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		timedOut bool
		want     string
	}{
		{"stalled handshake", context.DeadlineExceeded, false, checkerdef.NetFailureTLSHandshakeTimeout},
		{"caller deadline fired", errHandshakeEOF, true, checkerdef.NetFailureTLSHandshakeTimeout},
		{"net timeout", timeoutError{}, false, checkerdef.NetFailureTLSHandshakeTimeout},

		// The whole reason this is a separate function: a certificate verdict
		// is the server ANSWERING. Tracing the path would be noise.
		{"expired certificate", &x509.CertificateInvalidError{Reason: x509.Expired}, false, ""},
		{"hostname mismatch", x509.HostnameError{Host: "acme.com"}, false, ""},
		{"unknown authority", x509.UnknownAuthorityError{}, false, ""},
		{"nil", nil, false, ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := checkerdef.ClassifyTLSHandshakeError(test.err, test.timedOut); got != test.want {
				t.Fatalf("ClassifyTLSHandshakeError(%v, %v) = %q, want %q",
					test.err, test.timedOut, got, test.want)
			}
		})
	}
}

func TestNewNetworkFailureIsNilWithoutAClass(t *testing.T) {
	t.Parallel()

	if got := checkerdef.NewNetworkFailure("", "acme.com", "10.0.0.1", 443); got != nil {
		t.Fatalf("an unclassified failure produced a marker: %+v", got)
	}

	got := checkerdef.NewNetworkFailure(checkerdef.NetFailureConnectTimeout, "acme.com", "10.0.0.1", 443)
	if got == nil || got.Class != checkerdef.NetFailureConnectTimeout ||
		got.Address != "10.0.0.1" || got.Port != 443 || got.Host != "acme.com" {
		t.Fatalf("marker did not round-trip its endpoint: %+v", got)
	}
}

func TestSetLocateDropNetworkFailure(t *testing.T) {
	t.Parallel()

	result := &checkerdef.Result{}

	// A nil failure must not even allocate Diagnostics — otherwise every
	// successful probe would start carrying an empty block.
	result.SetNetworkFailure(nil)

	if result.Diagnostics != nil {
		t.Fatalf("a nil failure allocated diagnostics: %+v", result.Diagnostics)
	}

	// Locate on a result with no marker is a no-op, never an invention.
	checkerdef.LocateNetworkFailure(result, "acme.com", "10.0.0.1", 443)

	if result.Diagnostics != nil {
		t.Fatalf("locate invented a marker: %+v", result.Diagnostics)
	}

	result.SetNetworkFailure(checkerdef.NewNetworkFailure(checkerdef.NetFailureConnectionRefused, "", "", 0))
	checkerdef.LocateNetworkFailure(result, "acme.com", "10.0.0.1", 443)

	failure := result.Diagnostics.NetworkFailure
	if failure.Host != "acme.com" || failure.Address != "10.0.0.1" || failure.Port != 443 {
		t.Fatalf("locate did not stamp the endpoint: %+v", failure)
	}

	checkerdef.DropNetworkFailure(result)

	if result.Diagnostics.NetworkFailure != nil {
		t.Fatalf("drop left a marker behind: %+v", result.Diagnostics.NetworkFailure)
	}
}
