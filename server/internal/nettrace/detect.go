package nettrace

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
)

// ErrNoModeAvailable means this host can produce no trace at all for the given
// target: no ICMP socket of any kind, and no port to fall back to.
//
// It is a normal outcome, not a fault. An unprivileged container running an
// `icmp` check has nothing to trace with, and the correct response is to attach
// no capture — never to fail the incident.
var ErrNoModeAvailable = errors.New("nettrace: no probe mode available on this host")

// Capability is what this host can actually open, probed once at first use.
//
// DETECTION IS A SOCKET OPEN, NOT A GUESS. Reading /proc, checking uid, or
// parsing capability bits all get the answer wrong in a container: the only
// reliable test of "can I open a raw ICMP socket" is opening one and closing it
// again. It costs two syscalls, once per process.
type Capability struct {
	ICMPRawV4 bool
	ICMPRawV6 bool
	ICMPUDPV4 bool
	ICMPUDPV6 bool
}

// Any reports whether any ICMP mode is available at all.
func (c Capability) Any() bool {
	return c.ICMPRawV4 || c.ICMPRawV6 || c.ICMPUDPV4 || c.ICMPUDPV6
}

// ModeFor picks the best available mode for a target.
//
// The ladder is the spec's: privileged ICMP, then unprivileged ICMP, then TCP
// to the check's own port. It returns "" when nothing is possible, which is the
// signal to attach no capture.
func (c Capability) ModeFor(isV6 bool, port int) Mode {
	raw, udp := c.ICMPRawV4, c.ICMPUDPV4
	if isV6 {
		raw, udp = c.ICMPRawV6, c.ICMPUDPV6
	}

	switch {
	case raw:
		return ModeICMPRaw
	case udp:
		return ModeICMPUDP
	case port > 0:
		return ModeTCP
	default:
		return ""
	}
}

//nolint:gochecknoglobals // process-wide detection cache, by design
var detectOnce = sync.OnceValue(probeCapability)

// Detect returns this host's cached capability, probing on the first call.
//
// DEGRADE SILENTLY. Every failure here is an expected deployment shape — an
// unprivileged container, a locked-down ping_group_range, a host with no IPv6 —
// and none of them is worth a log line on every trace, let alone an error that
// could reach the check path.
func Detect() Capability {
	return detectOnce()
}

func probeCapability() Capability {
	return Capability{
		ICMPRawV4: canListen(ModeICMPRaw, false),
		ICMPRawV6: canListen(ModeICMPRaw, true),
		ICMPUDPV4: canListen(ModeICMPUDP, false),
		ICMPUDPV6: canListen(ModeICMPUDP, true),
	}
}

func canListen(mode Mode, isV6 bool) bool {
	prober, err := newICMPProber(mode, isV6)
	if err != nil {
		return false
	}

	_ = prober.Close()

	return true
}

// NewProber builds the prober for a mode. Callers normally use Run, which picks
// the mode itself.
func NewProber(mode Mode, isV6 bool, port int) (Prober, error) {
	switch mode {
	case ModeICMPRaw, ModeICMPUDP:
		return newICMPProber(mode, isV6)
	case ModeTCP:
		return newTCPProber(port, isV6)
	default:
		return nil, fmt.Errorf("%w: %q", ErrNoModeAvailable, mode)
	}
}

// Run is the whole capture in one call: detect, build the best prober for the
// target, trace, close.
//
// It returns ErrNoModeAvailable when this host cannot probe at all. Callers
// treat that as "no capture", never as a failure of anything upstream.
func Run(ctx context.Context, opts *Options) (*Capture, error) {
	if opts.Address == nil {
		return nil, ErrNoTarget
	}

	isV6 := opts.Address.To4() == nil

	mode := Detect().ModeFor(isV6, opts.Port)
	if mode == "" {
		return nil, ErrNoModeAvailable
	}

	prober, err := NewProber(mode, isV6, opts.Port)
	if err != nil {
		return nil, err
	}

	defer func() { _ = prober.Close() }()

	if opts.Resolver == nil {
		opts.Resolver = net.DefaultResolver
	}

	return Trace(ctx, prober, opts)
}
