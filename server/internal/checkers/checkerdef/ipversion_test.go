package checkerdef_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// legacyPickIPv4First is the address-pick loop that was copy-pasted across the
// nine address-picking checkers before SelectIPAddr existed. It is kept here
// verbatim as the ORACLE for the backward-compatibility test: `auto` must agree
// with it on every input, because disagreeing would silently change which
// address every existing check in every install probes.
func legacyPickIPv4First(addrs []net.IPAddr) net.IP {
	var targetIP net.IP

	for i := range addrs {
		if addrs[i].IP.To4() != nil {
			targetIP = addrs[i].IP

			break
		}
	}

	if targetIP == nil {
		targetIP = addrs[0].IP
	}

	return targetIP
}

// errNotAnEgressFailure stands in for any error that is not the worker-egress
// sentinel, to prove the status mapping is about that sentinel specifically.
var errNotAnEgressFailure = errors.New("no address of the requested IP version: x")

func ipAddrs(ips ...string) []net.IPAddr {
	addrs := make([]net.IPAddr, 0, len(ips))
	for _, ip := range ips {
		addrs = append(addrs, net.IPAddr{IP: net.ParseIP(ip)})
	}

	return addrs
}

func TestParseIPVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    checkerdef.IPVersion
		wantErr bool
	}{
		{name: "empty is auto", raw: "", want: checkerdef.IPVersionAuto},
		{name: "auto", raw: "auto", want: checkerdef.IPVersionAuto},
		{name: "ipv4", raw: "ipv4", want: checkerdef.IPVersionIPv4},
		{name: "ipv6", raw: "ipv6", want: checkerdef.IPVersionIPv6},
		{name: "case insensitive", raw: "IPv6", want: checkerdef.IPVersionIPv6},
		{name: "whitespace tolerated", raw: "  ipv4 ", want: checkerdef.IPVersionIPv4},
		// No aliases are invented: a typo must fail loudly at write time rather
		// than silently monitoring the wrong family.
		{name: "v6 alias rejected", raw: "v6", wantErr: true},
		{name: "bare 4 rejected", raw: "4", wantErr: true},
		{name: "inet rejected", raw: "inet", wantErr: true},
		{name: "both rejected", raw: "both", wantErr: true},
		{name: "null rejected", raw: "null", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			got, err := checkerdef.ParseIPVersion(tt.raw)
			if tt.wantErr {
				r.Error(err)
				r.ErrorIs(err, checkerdef.ErrInvalidIPVersion)
				r.Contains(err.Error(), "auto, ipv4 or ipv6")

				return
			}

			r.NoError(err)
			r.Equal(tt.want, got)
		})
	}
}

func TestIPVersionFromConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  map[string]any
		want    checkerdef.IPVersion
		wantErr bool
	}{
		{name: "nil config", config: nil, want: checkerdef.IPVersionAuto},
		{name: "absent key", config: map[string]any{"host": "example.com"}, want: checkerdef.IPVersionAuto},
		{name: "nil value", config: map[string]any{"ipVersion": nil}, want: checkerdef.IPVersionAuto},
		{name: "camelCase canonical", config: map[string]any{"ipVersion": "ipv6"}, want: checkerdef.IPVersionIPv6},
		{name: "snake_case fallback", config: map[string]any{"ip_version": "ipv4"}, want: checkerdef.IPVersionIPv4},
		{
			name:   "camelCase wins over snake_case",
			config: map[string]any{"ipVersion": "ipv6", "ip_version": "ipv4"},
			want:   checkerdef.IPVersionIPv6,
		},
		{name: "wrong type", config: map[string]any{"ipVersion": 6}, wantErr: true},
		{name: "invalid value", config: map[string]any{"ipVersion": "v6"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			got, err := checkerdef.IPVersionFromConfig(tt.config)
			if tt.wantErr {
				r.ErrorIs(err, checkerdef.ErrInvalidIPVersion)

				return
			}

			r.NoError(err)
			r.Equal(tt.want, got)
		})
	}
}

func TestIPVersionContext(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// The overwhelmingly common case: no family on the context means auto, so
	// every checker keeps its historical behavior without any plumbing.
	r.Equal(checkerdef.IPVersionAuto, checkerdef.IPVersionFrom(context.Background()))

	ctx := checkerdef.WithIPVersion(context.Background(), checkerdef.IPVersionIPv6)
	r.Equal(checkerdef.IPVersionIPv6, checkerdef.IPVersionFrom(ctx))
	r.True(checkerdef.IPVersionFrom(ctx).Explicit())
}

func TestIPVersionHelpers(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("tcp", checkerdef.IPVersionAuto.Network("tcp"))
	r.Equal("tcp4", checkerdef.IPVersionIPv4.Network("tcp"))
	r.Equal("tcp6", checkerdef.IPVersionIPv6.Network("tcp"))
	r.Equal("udp6", checkerdef.IPVersionIPv6.Network("udp"))

	r.False(checkerdef.IPVersionAuto.Explicit())
	r.True(checkerdef.IPVersionIPv4.Explicit())
	r.True(checkerdef.IPVersionIPv6.Explicit())

	r.Equal(checkerdef.IPVersionIPv4, checkerdef.IPVersionOf(net.ParseIP("1.2.3.4")))
	// An IPv4 address in its 16-byte representation is still IPv4.
	r.Equal(checkerdef.IPVersionIPv4, checkerdef.IPVersionOf(net.ParseIP("::ffff:1.2.3.4")))
	r.Equal(checkerdef.IPVersionIPv6, checkerdef.IPVersionOf(net.ParseIP("2001:db8::1")))

	r.True(checkerdef.MatchesIPVersion(net.ParseIP("1.2.3.4"), checkerdef.IPVersionAuto))
	r.True(checkerdef.MatchesIPVersion(net.ParseIP("2001:db8::1"), checkerdef.IPVersionAuto))
	r.False(checkerdef.MatchesIPVersion(net.ParseIP("1.2.3.4"), checkerdef.IPVersionIPv6))
	r.False(checkerdef.MatchesIPVersion(net.ParseIP("2001:db8::1"), checkerdef.IPVersionIPv4))
}

// TestSelectIPAddr_AutoMatchesLegacyLoop is the backward-compatibility guard:
// `auto` must reproduce the old IPv4-first loop byte-for-byte on every ordering
// of every mix of families. If this ever fails, every existing check on a
// dual-stack target silently changed which address it probes.
func TestSelectIPAddr_AutoMatchesLegacyLoop(t *testing.T) {
	t.Parallel()

	cases := [][]string{
		{"1.2.3.4"},
		{"2001:db8::1"},
		{"1.2.3.4", "5.6.7.8"},
		{"2001:db8::1", "2001:db8::2"},
		// Dual-stack, both orderings: IPv4 wins regardless of resolver order.
		{"1.2.3.4", "2001:db8::1"},
		{"2001:db8::1", "1.2.3.4"},
		{"2001:db8::1", "2001:db8::2", "1.2.3.4"},
		{"::ffff:1.2.3.4", "2001:db8::1"},
	}

	for _, ips := range cases {
		t.Run(strings.Join(ips, ","), func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			addrs := ipAddrs(ips...)

			got, err := checkerdef.SelectIPAddr("example.com", addrs, checkerdef.IPVersionAuto)
			r.NoError(err)

			want := legacyPickIPv4First(addrs)
			r.True(want.Equal(got), "auto picked %s, legacy loop picked %s", got, want)

			// The empty string is the zero value of IPVersion and must behave
			// exactly like auto — a config that never mentions the option.
			gotZero, err := checkerdef.SelectIPAddr("example.com", addrs, "")
			r.NoError(err)
			r.True(want.Equal(gotZero))
		})
	}
}

// TestSelectIPAddr_AutoNeverProbesEgress pins the other half of the
// compatibility promise: an auto check performs exactly the syscalls it always
// did, so the local-egress pre-flight must not run for it.
func TestSelectIPAddr_AutoNeverProbesEgress(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	probed := false
	probe := func(_ checkerdef.IPVersion, _ net.IP) error {
		probed = true

		return nil
	}

	_, err := checkerdef.SelectIPAddrWithProbe(
		"example.com", ipAddrs("1.2.3.4", "2001:db8::1"), checkerdef.IPVersionAuto, probe,
	)
	r.NoError(err)
	r.False(probed, "auto must not perform the local-egress pre-flight")

	// Positive control: a pinned family DOES probe, so the assertion above is
	// about auto rather than about a probe that never runs at all.
	_, err = checkerdef.SelectIPAddrWithProbe(
		"example.com", ipAddrs("1.2.3.4", "2001:db8::1"), checkerdef.IPVersionIPv6, probe,
	)
	r.NoError(err)
	r.True(probed)
}

func TestSelectIPAddr_PinnedFamily(t *testing.T) {
	t.Parallel()

	noProbe := func(_ checkerdef.IPVersion, _ net.IP) error { return nil }

	tests := []struct {
		name    string
		ips     []string
		version checkerdef.IPVersion
		want    string
	}{
		{
			name:    "ipv6 on a dual-stack target picks the AAAA address",
			ips:     []string{"1.2.3.4", "2001:db8::1"},
			version: checkerdef.IPVersionIPv6,
			want:    "2001:db8::1",
		},
		{
			name:    "ipv4 on a dual-stack target picks the A address",
			ips:     []string{"2001:db8::1", "1.2.3.4"},
			version: checkerdef.IPVersionIPv4,
			want:    "1.2.3.4",
		},
		{
			name:    "ipv6 skips leading IPv4 addresses",
			ips:     []string{"1.2.3.4", "5.6.7.8", "2001:db8::9"},
			version: checkerdef.IPVersionIPv6,
			want:    "2001:db8::9",
		},
		{
			name:    "ipv4 accepts a v4-mapped address",
			ips:     []string{"2001:db8::1", "::ffff:1.2.3.4"},
			version: checkerdef.IPVersionIPv4,
			want:    "1.2.3.4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			got, err := checkerdef.SelectIPAddrWithProbe("example.com", ipAddrs(tt.ips...), tt.version, noProbe)
			r.NoError(err)
			r.Equal(tt.want, got.String())
			r.Equal(tt.version, checkerdef.IPVersionOf(got))
		})
	}
}

// TestSelectIPAddr_NoAddressForFamily proves the pinned family NEVER falls back
// — falling back is exactly what makes an "IPv6 check" silently an IPv4 check —
// and that the failure is cataloged and names the host and the family.
func TestSelectIPAddr_NoAddressForFamily(t *testing.T) {
	t.Parallel()

	noProbe := func(_ checkerdef.IPVersion, _ net.IP) error { return nil }

	t.Run("ipv6 requested, only A records", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		_, err := checkerdef.SelectIPAddrWithProbe(
			"v4only.example.com", ipAddrs("1.2.3.4", "5.6.7.8"), checkerdef.IPVersionIPv6, noProbe,
		)
		r.Error(err)
		r.ErrorIs(err, checkerdef.ErrNoAddressForFamily)
		r.NotErrorIs(err, checkerdef.ErrWorkerNoEgress)
		r.Contains(err.Error(), "v4only.example.com")
		r.Contains(err.Error(), "IPv6")
		r.Contains(err.Error(), "AAAA")
		// It must not read as a generic dial failure.
		r.NotContains(err.Error(), "connection refused")
	})

	t.Run("ipv4 requested, only AAAA records", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		_, err := checkerdef.SelectIPAddrWithProbe(
			"v6only.example.com", ipAddrs("2001:db8::1"), checkerdef.IPVersionIPv4, noProbe,
		)
		r.ErrorIs(err, checkerdef.ErrNoAddressForFamily)
		r.Contains(err.Error(), "v6only.example.com")
		r.Contains(err.Error(), "IPv4")
		r.Contains(err.Error(), " A record")
	})

	t.Run("no addresses at all", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		_, err := checkerdef.SelectIPAddrWithProbe(
			"empty.example.com", nil, checkerdef.IPVersionAuto, noProbe,
		)
		r.ErrorIs(err, checkerdef.ErrNoAddressForFamily)
	})
}

// TestSelectIPAddr_WorkerNoEgress covers the settled region decision: a worker
// that cannot originate the requested family fails the check with its OWN
// error, worded so the reader looks at their region rather than at their
// service.
func TestSelectIPAddr_WorkerNoEgress(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Stand in for a node with no IPv6 route: the pre-flight fails.
	probe := func(version checkerdef.IPVersion, _ net.IP) error {
		return checkerdef.DialEgressProbe(version, net.ParseIP("::"))
	}

	_, err := checkerdef.SelectIPAddrWithProbe(
		"dual.example.com", ipAddrs("1.2.3.4", "2001:db8::1"), checkerdef.IPVersionIPv6, probe,
	)

	// The probe against "::" may legitimately succeed on a dual-stack host, so
	// exercise the message itself through the production error builder, which is
	// what the user reads.
	if err == nil {
		err = wrapNoEgress(t)
	}

	r.ErrorIs(err, checkerdef.ErrWorkerNoEgress)
	r.NotErrorIs(err, checkerdef.ErrNoAddressForFamily)

	message := err.Error()
	// The wording must blame the WORKER, not the target: a user reading it once
	// has to know to look at their region.
	r.Contains(message, "worker")
	r.Contains(message, "IPv6")
	r.Contains(message, "region")
	r.Contains(message, "the target is probably fine")

	// And it must be told apart from the target-has-no-AAAA case, which is a
	// different fix entirely.
	r.NotContains(message, "AAAA record")

	// A worker-side gap is not the target being down.
	r.Equal(checkerdef.StatusError, checkerdef.IPVersionFailureStatus(err))
	r.Equal(
		checkerdef.StatusDown,
		checkerdef.IPVersionFailureStatus(errNotAnEgressFailure),
	)
}

// wrapNoEgress produces the production worker-no-egress error deterministically,
// for hosts where a UDP route lookup to "::" happens to succeed.
func wrapNoEgress(t *testing.T) error {
	t.Helper()

	// 100::/64 is the RFC 6666 discard-only prefix; on a host with IPv6 it is
	// routable, so force the failure through an address family the kernel
	// refuses instead.
	err := checkerdef.DialEgressProbe(checkerdef.IPVersionIPv6, net.ParseIP("1.2.3.4"))
	if err != nil {
		return err
	}

	t.Skip("host accepts every UDP route lookup; cannot exercise the no-egress branch here")

	return nil
}

func TestDialEgressProbe_AutoIsNoop(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// auto must never touch the network stack.
	r.NoError(checkerdef.DialEgressProbe(checkerdef.IPVersionAuto, nil))
}

func TestSampleConfigWithIPVersion(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := checkerdef.SampleConfigWithIPVersion(map[string]any{"host": "example.com"}, checkerdef.IPVersionIPv6)
	r.Equal("ipv6", cfg[checkerdef.IPVersionConfigKey])

	version, err := checkerdef.IPVersionFromConfig(cfg)
	r.NoError(err)
	r.Equal(checkerdef.IPVersionIPv6, version)
}

// TestSupportsIPVersionMetadata pins the declared capability set, including the
// deliberate exclusions: `dns` is scoped out (the option has two incompatible
// meanings there) and the passive types have no network target at all.
func TestSupportsIPVersionMetadata(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	supported := map[checkerdef.CheckType]bool{
		checkerdef.CheckTypeHTTP: true, checkerdef.CheckTypeTCP: true,
		checkerdef.CheckTypeUDP: true, checkerdef.CheckTypeICMP: true,
		checkerdef.CheckTypeSSL: true, checkerdef.CheckTypeSSH: true,
		checkerdef.CheckTypeSMTP: true, checkerdef.CheckTypeIMAP: true,
		checkerdef.CheckTypePOP3: true, checkerdef.CheckTypeDNSBL: true,
	}

	for _, meta := range checkerdef.ListCheckTypeMetas() {
		r.Equal(supported[meta.Type], meta.SupportsIPVersion, "check type %s", meta.Type)
	}

	// Named explicitly because it is a settled product decision, not an
	// oversight: DNS checks do not take ipVersion.
	dns := checkerdef.GetCheckTypeMeta(checkerdef.CheckTypeDNS)
	r.NotNil(dns)
	r.False(dns.SupportsIPVersion)
}
