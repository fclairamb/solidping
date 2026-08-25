package checkicmp

import (
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// errAllPingsSilent stands in for the ordinary total-loss case: the echoes
// simply went unanswered, with no error at all from the socket.
var (
	errAllPingsSilent = errors.New("request timed out")
	errUnclassified   = errors.New("something nobody classified")
)

// unreachableErr wraps an errno the way the ping path surfaces one.
func unreachableErr(errno syscall.Errno) error {
	return &net.OpError{Op: "write", Net: "ip4:icmp", Err: errno}
}

// TestICMPFailureClass pins the one distinction that changes how a reader
// interprets the trace: silence usually means the trace also stops short of the
// target, while an explicit unreachable means a router made a decision and the
// trace should show which one.
func TestICMPFailureClass(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		results []pingResult
		want    string
	}{
		{
			"total silence",
			[]pingResult{{}, {Error: errAllPingsSilent}, {}},
			checkerdef.NetFailureICMPTimeout,
		},
		{
			"no errors recorded at all",
			[]pingResult{{}, {}, {}},
			checkerdef.NetFailureICMPTimeout,
		},
		{
			"a router said host unreachable",
			[]pingResult{{}, {Error: unreachableErr(syscall.EHOSTUNREACH)}, {}},
			checkerdef.NetFailureICMPUnreachable,
		},
		{
			"a router said network unreachable",
			[]pingResult{{Error: unreachableErr(syscall.ENETUNREACH)}},
			checkerdef.NetFailureICMPUnreachable,
		},
		{
			"one unreachable among timeouts still wins",
			[]pingResult{
				{Error: errAllPingsSilent},
				{Error: unreachableErr(syscall.EHOSTUNREACH)},
				{Error: errAllPingsSilent},
			},
			checkerdef.NetFailureICMPUnreachable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := icmpFailureClass(test.results); got != test.want {
				t.Fatalf("icmpFailureClass = %q, want %q", got, test.want)
			}
		})
	}
}

// TestICMPFailureClassIsNeverEmpty is the property the incident pipeline relies
// on: an ICMP check that lost EVERY packet is by definition a reachability
// failure, so this must always name a class — a "" would silently disable the
// trace for the one check type the feature is most obviously for.
func TestICMPFailureClassIsNeverEmpty(t *testing.T) {
	t.Parallel()

	for _, results := range [][]pingResult{
		nil,
		{},
		{{Error: errUnclassified}},
	} {
		if got := icmpFailureClass(results); got == "" {
			t.Fatalf("icmpFailureClass(%v) returned no class", results)
		}
	}
}
