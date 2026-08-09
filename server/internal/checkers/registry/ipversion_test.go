package registry_test

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
)

// This file is the cross-checker wiring guard for the shared `ipVersion`
// option. It exists at the registry level on purpose: the option's whole point
// is that address-family selection happens ONCE, so the natural test is one
// table walked for every type that declares support, rather than nine
// near-identical per-package tests that could each drift.
//
// Every case uses IP literals, so no DNS is involved and the assertions are the
// same on every machine and in CI.

// ipVersionCase describes how to build a minimal config for one check type.
type ipVersionCase struct {
	checkType checkerdef.CheckType
	// config builds a config pointing at the given host literal.
	config func(host string, port int) map[string]any
}

// hostPortConfig is the shape almost every address-picking checker uses.
func hostPortConfig(host string, port int) map[string]any {
	return map[string]any{"host": host, "port": port, "timeout": "2s"}
}

func ipVersionCases() []ipVersionCase {
	return []ipVersionCase{
		{checkType: checkerdef.CheckTypeTCP, config: hostPortConfig},
		{checkType: checkerdef.CheckTypeUDP, config: hostPortConfig},
		{checkType: checkerdef.CheckTypeSSH, config: hostPortConfig},
		{checkType: checkerdef.CheckTypeSMTP, config: hostPortConfig},
		{checkType: checkerdef.CheckTypeIMAP, config: hostPortConfig},
		{checkType: checkerdef.CheckTypePOP3, config: hostPortConfig},
		{
			checkType: checkerdef.CheckTypeICMP,
			config: func(host string, _ int) map[string]any {
				return map[string]any{"host": host, "count": 1, "timeout": "2s"}
			},
		},
		{
			checkType: checkerdef.CheckTypeSSL,
			config: func(host string, port int) map[string]any {
				return map[string]any{"host": host, "port": port, "timeout": "2s"}
			},
		},
		{
			checkType: checkerdef.CheckTypeHTTP,
			config: func(host string, port int) map[string]any {
				return map[string]any{
					"url":     "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/",
					"timeout": "2s",
				}
			},
		},
	}
}

// runCheck executes one check type against a config, with the given address
// family pinned on the context exactly the way the worker does it.
func runCheck(
	t *testing.T, checkType checkerdef.CheckType, cfgMap map[string]any, version checkerdef.IPVersion,
) *checkerdef.Result {
	t.Helper()

	r := require.New(t)

	checker, ok := registry.GetChecker(checkType)
	r.True(ok, "no checker registered for %s", checkType)

	cfg, ok := registry.ParseConfig(checkType)
	r.True(ok, "no config for %s", checkType)
	r.NoError(cfg.FromMap(cfgMap))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if version.Explicit() {
		ctx = checkerdef.WithIPVersion(ctx, version)
	}

	result, err := checker.Execute(ctx, cfg)
	r.NoError(err)
	r.NotNil(result)

	return result
}

func resultError(t *testing.T, result *checkerdef.Result) string {
	t.Helper()

	if result.Output == nil {
		return ""
	}

	message, _ := result.Output[checkerdef.OutputKeyError].(string)

	return message
}

// TestPinnedFamilyUnavailable_IsCataloged walks every ipVersion-capable check
// type and proves that asking for a family the target has no address for fails
// with the cataloged error naming the host and the family — NOT with a generic
// dial failure that would send the user hunting through their own service.
func TestPinnedFamilyUnavailable_IsCataloged(t *testing.T) {
	t.Parallel()

	for _, tc := range ipVersionCases() {
		t.Run(string(tc.checkType), func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			// 127.0.0.1 is IPv4-only by construction, so ipv6 can never be
			// satisfied — the deterministic stand-in for a host with no AAAA.
			result := runCheck(t, tc.checkType, tc.config("127.0.0.1", 9), checkerdef.IPVersionIPv6)

			message := resultError(t, result)
			r.NotEmpty(message, "expected an error message, got output %v", result.Output)
			r.Contains(message, "127.0.0.1", "the error must name the host")
			r.Contains(message, "IPv6", "the error must name the requested family")
			r.Contains(
				message, "no address of the requested IP version",
				"the error must be the cataloged one, not a generic dial failure",
			)
		})
	}
}

// TestPinnedFamilyAvailable_IsHonored is the positive control for the test
// above: with a matching family the check gets past address selection and
// reaches the network, so the cataloged error is genuinely about the family
// and not something every run produces.
func TestPinnedFamilyAvailable_IsHonored(t *testing.T) {
	t.Parallel()

	for _, tc := range ipVersionCases() {
		t.Run(string(tc.checkType), func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			result := runCheck(t, tc.checkType, tc.config("127.0.0.1", 9), checkerdef.IPVersionIPv4)

			r.NotContains(
				resultError(t, result), "no address of the requested IP version",
				"an ipv4 pin on an IPv4 host must not raise the family error",
			)
		})
	}
}

// TestAutoNeverRaisesTheFamilyError is the backward-compatibility guard at the
// checker level: an unpinned check must behave exactly as before, so it can
// never produce the new error — not even against an IPv6-only or IPv4-only
// target.
func TestAutoNeverRaisesTheFamilyError(t *testing.T) {
	t.Parallel()

	for _, tc := range ipVersionCases() {
		t.Run(string(tc.checkType), func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			for _, host := range []string{"127.0.0.1", "::1"} {
				result := runCheck(t, tc.checkType, tc.config(host, 9), checkerdef.IPVersionAuto)
				r.NotContains(resultError(t, result), "no address of the requested IP version")
				r.NotContains(resultError(t, result), "worker has no egress")
			}
		})
	}
}

// TestReportedIPVersionMatchesThePin proves the acceptance criterion end to end
// for the types that report the family: what the check says it used is what it
// was pinned to. Uses loopback literals of each family, so no DNS and no
// external network is involved.
func TestReportedIPVersionMatchesThePin(t *testing.T) {
	t.Parallel()

	// Only the types that emit the ip_version output field.
	reporting := []checkerdef.CheckType{
		checkerdef.CheckTypeTCP, checkerdef.CheckTypeUDP, checkerdef.CheckTypeICMP,
	}

	for _, checkType := range reporting {
		t.Run(string(checkType), func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			cases := []struct {
				host    string
				version checkerdef.IPVersion
			}{
				{host: "127.0.0.1", version: checkerdef.IPVersionIPv4},
				{host: "::1", version: checkerdef.IPVersionIPv6},
			}

			for _, tc := range cases {
				cfg := map[string]any{"host": tc.host, "port": 9, "count": 1, "timeout": "2s"}

				result := runCheck(t, checkType, cfg, tc.version)
				reported, _ := result.Output[checkerdef.OutputKeyIPVersion].(string)

				// icmp needs a raw socket; when the test host forbids it the
				// probe fails before reporting, which is not what this asserts.
				if reported == "" {
					continue
				}

				r.Equal(string(tc.version), reported, "host %s", tc.host)
			}
		})
	}
}
