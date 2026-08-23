package domainverify

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// errTestLookup is a static stub error for lookup-failure cases.
var errTestLookup = errors.New("stub lookup failure")

func TestNormalize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "plain host", in: "status.acme.com", want: "status.acme.com"},
		{name: "uppercase", in: "Status.ACME.com", want: "status.acme.com"},
		{name: "trailing dot", in: "status.acme.com.", want: "status.acme.com"},
		{name: "surrounding space", in: "  status.acme.com  ", want: "status.acme.com"},
		{name: "idn to punycode", in: "stätus.acme.com", want: "xn--sttus-hra.acme.com"},
		{name: "empty", in: "", wantErr: true},
		{name: "single label", in: "localhost", wantErr: true},
		{name: "ipv4", in: "192.168.0.1", wantErr: true},
		{name: "ipv6", in: "::1", wantErr: true},
		{name: "wildcard", in: "*.acme.com", wantErr: true},
		{name: "with port", in: "status.acme.com:8080", wantErr: true},
		{name: "with scheme", in: "https://status.acme.com", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			got, err := Normalize(tc.in)
			if tc.wantErr {
				r.Error(err)
				r.ErrorIs(err, ErrInvalidDomain)

				return
			}

			r.NoError(err)
			r.Equal(tc.want, got)
		})
	}
}

func TestParseMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		in     string
		want   Mode
		wantOK bool
	}{
		{name: "empty defaults to shared", in: "", want: ModeShared, wantOK: true},
		{name: "shared", in: "shared", want: ModeShared, wantOK: true},
		{name: "token", in: "token", want: ModeToken, wantOK: true},
		{name: "uppercase token", in: "TOKEN", want: ModeToken, wantOK: true},
		{name: "padded", in: "  token  ", want: ModeToken, wantOK: true},
		{name: "unknown rejected", in: "wildcard", want: ModeShared, wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			got, ok := ParseMode(tc.in)
			r.Equal(tc.want, got)
			r.Equal(tc.wantOK, ok)
			r.True(got.Valid())
		})
	}
}

func TestTokenHost(t *testing.T) {
	t.Parallel()

	longToken := strings.Repeat("a", maxLabelLen+1)

	tests := []struct {
		name   string
		token  string
		target string
		want   string
	}{
		{name: "nominal", token: "spabc123", target: "solidping.io", want: "spabc123.cname.solidping.io"},
		{name: "uppercase normalized", token: "SPABC", target: "SolidPing.IO", want: "spabc.cname.solidping.io"},
		{name: "trailing dot target", token: "spabc", target: "solidping.io.", want: "spabc.cname.solidping.io"},
		{name: "empty token", token: "", target: "solidping.io", want: ""},
		{name: "empty target", token: "spabc", target: "", want: ""},
		{name: "token too long", token: longToken, target: "solidping.io", want: ""},
		{name: "token with dot", token: "sp.abc", target: "solidping.io", want: ""},
		{name: "token with underscore", token: "sp_abc", target: "solidping.io", want: ""},
		{name: "token leading hyphen", token: "-spabc", target: "solidping.io", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			got := TokenHost(tc.token, tc.target)
			r.Equal(tc.want, got)

			if got != "" {
				// The built host must stay a legal hostname overall.
				r.LessOrEqual(len(got), maxDomainLen)
				for _, label := range strings.Split(got, ".") {
					r.LessOrEqual(len(label), maxLabelLen)
					r.NotEmpty(label)
				}
			}
		})
	}
}

func TestExpectedTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mode   Mode
		token  string
		target string
		want   string
	}{
		{name: "shared ignores token", mode: ModeShared, token: "spabc", target: "solidping.io", want: "solidping.io"},
		{name: "shared without token", mode: ModeShared, token: "", target: "solidping.io", want: "solidping.io"},
		{name: "shared without target", mode: ModeShared, token: "spabc", target: "", want: ""},
		{
			name: "token builds per-page host", mode: ModeToken, token: "spabc",
			target: "solidping.io", want: "spabc.cname.solidping.io",
		},
		{name: "token without token", mode: ModeToken, token: "", target: "solidping.io", want: ""},
		{name: "token without target", mode: ModeToken, token: "spabc", target: "", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			r.Equal(tc.want, ExpectedTarget(tc.mode, tc.token, tc.target))
		})
	}
}

// TestRecordsIsSingleCNAME is the regression guard for the v0.8.0 contract: a
// customer must create exactly ONE record, and it is never a TXT challenge.
func TestRecordsIsSingleCNAME(t *testing.T) {
	t.Parallel()

	t.Run("shared", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		records := Records("status.acme.com", "spabc", "cname.solidping.io", ModeShared)
		r.Len(records, 1)
		r.Equal("CNAME", records[0].Type)
		r.Equal("status.acme.com", records[0].Name)
		r.Equal("cname.solidping.io", records[0].Value)
	})

	t.Run("token", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		records := Records("status.acme.com", "spabc", "solidping.io", ModeToken)
		r.Len(records, 1)
		r.Equal("CNAME", records[0].Type)
		r.Equal("status.acme.com", records[0].Name)
		r.Equal("spabc.cname.solidping.io", records[0].Value)
	})

	t.Run("no target yields no records", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		r.Empty(Records("status.acme.com", "spabc", "", ModeShared))
		r.Empty(Records("status.acme.com", "", "cname.solidping.io", ModeToken))
	})
}

func TestCheckCNAME(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cname    string
		expected string
		lookErr  error
		wantOK   bool
		wantErr  bool
	}{
		{name: "exact", cname: "cname.solidping.io", expected: "cname.solidping.io", wantOK: true},
		{name: "trailing dot in answer", cname: "cname.solidping.io.", expected: "cname.solidping.io", wantOK: true},
		{name: "trailing dot in expected", cname: "cname.solidping.io", expected: "cname.solidping.io.", wantOK: true},
		{name: "case insensitive", cname: "CNAME.SolidPing.io", expected: "cname.solidping.io", wantOK: true},
		{name: "wrong target", cname: "elsewhere.example.com", expected: "cname.solidping.io", wantOK: false},
		// LookupCNAME returns the CANONICAL name of the chain: a customer host
		// chained through an intermediate still reports the final name, so a
		// wildcard CNAME under the token host would be invisible. This asserts
		// the canonical answer is what we compare.
		{name: "canonical of a chain", cname: "final.elsewhere.net", expected: "cname.solidping.io", wantOK: false},
		{name: "empty expected never matches", cname: "cname.solidping.io", expected: "", wantOK: false},
		{name: "nxdomain", expected: "cname.solidping.io", lookErr: errTestLookup, wantOK: false, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			called := false
			v := &Verifier{
				LookupCNAME: func(_ context.Context, host string) (string, error) {
					called = true
					r.Equal("status.acme.com", host)

					return tc.cname, tc.lookErr
				},
			}

			ok, err := v.CheckCNAME(t.Context(), "status.acme.com", tc.expected)
			r.Equal(tc.wantOK, ok)

			if tc.wantErr {
				r.Error(err)
			} else {
				r.NoError(err)
			}

			// An empty expected target must short-circuit before any DNS query.
			if tc.expected == "" {
				r.False(called)
			}
		})
	}
}

func TestVerifyModeMatrix(t *testing.T) {
	t.Parallel()

	const (
		target    = "solidping.io"
		token     = "spabc123"
		tokenHost = "spabc123.cname.solidping.io"
	)

	tests := []struct {
		name    string
		mode    Mode
		token   string
		target  string
		cname   string
		lookErr error
		want    bool
	}{
		{name: "shared: exact target", mode: ModeShared, token: token, target: target, cname: target, want: true},
		{
			name: "shared: token host does not verify", mode: ModeShared, token: token,
			target: target, cname: tokenHost, want: false,
		},
		{
			name: "shared: wrong target", mode: ModeShared, token: token, target: target,
			cname: "other.example.com", want: false,
		},
		{
			name: "shared: nxdomain", mode: ModeShared, token: token, target: target,
			lookErr: errTestLookup, want: false,
		},
		{name: "token: token host", mode: ModeToken, token: token, target: target, cname: tokenHost, want: true},
		// The no-dual-accept rule: the shared target must NOT verify in token
		// mode, otherwise the takeover protection is nullified.
		{
			name: "token: shared target rejected", mode: ModeToken, token: token,
			target: target, cname: target, want: false,
		},
		{
			name: "token: another page's token rejected", mode: ModeToken, token: token,
			target: target, cname: "spother.cname.solidping.io", want: false,
		},
		{name: "token: missing token", mode: ModeToken, token: "", target: target, cname: target, want: false},
		{name: "token: nxdomain", mode: ModeToken, token: token, target: target, lookErr: errTestLookup, want: false},
		{name: "no configured target", mode: ModeShared, token: token, target: "", cname: target, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			v := &Verifier{
				LookupCNAME: func(_ context.Context, _ string) (string, error) {
					return tc.cname, tc.lookErr
				},
			}

			got := v.Verify(t.Context(), "status.acme.com", tc.token, tc.target, tc.mode)
			r.Equal(tc.want, got)
		})
	}
}

// TestNoTXTSurfaceRemains is a compile-time-ish guard: the TXT challenge API is
// gone, so nothing in the package may still emit a TXT record.
func TestNoTXTSurfaceRemains(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, mode := range []Mode{ModeShared, ModeToken} {
		for _, rec := range Records("status.acme.com", "spabc", "cname.solidping.io", mode) {
			r.NotEqual("TXT", rec.Type)
		}
	}
}

// TestDiagnoseRecordsWhatItSaw covers the diagnosability directive of spec
// 2026-08-23-03. A bare false has meant, at various times, a wrong CNAME, an
// NXDOMAIN, an installation with no CNAME target configured, and a page checked
// in the wrong mode — and telling them apart required shell access to the pod.
func TestDiagnoseRecordsWhatItSaw(t *testing.T) {
	t.Parallel()

	t.Run("mismatch names both sides", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		v := &Verifier{LookupCNAME: func(context.Context, string) (string, error) {
			return "elsewhere.example.net.", nil
		}}

		diag := v.Diagnose(t.Context(), "status.acme.com", "spabc", "cname.solidping.io", ModeShared)
		r.False(diag.OK)
		r.Equal("cname.solidping.io", diag.Expected)
		r.Equal("elsewhere.example.net", diag.Resolved)
		r.Equal(ModeShared, diag.Mode)
		r.Contains(diag.String(), "expected=cname.solidping.io")
		r.Contains(diag.String(), "resolved=elsewhere.example.net")
	})

	t.Run("lookup error is preserved", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		v := &Verifier{LookupCNAME: func(context.Context, string) (string, error) {
			return "", errTestLookup
		}}

		diag := v.Diagnose(t.Context(), "status.acme.com", "spabc", "cname.solidping.io", ModeShared)
		r.False(diag.OK)
		r.ErrorIs(diag.Err, errTestLookup)
		r.Contains(diag.String(), "error=")
	})

	t.Run("no configured target says so instead of blaming DNS", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		called := false
		v := &Verifier{LookupCNAME: func(context.Context, string) (string, error) {
			called = true

			return "cname.solidping.io.", nil
		}}

		diag := v.Diagnose(t.Context(), "status.acme.com", "spabc", "", ModeShared)
		r.False(diag.OK)
		r.False(called, "DNS is never consulted when nothing could have matched")
		r.Contains(diag.String(), "no CNAME target configured")
	})

	t.Run("token mode records the per-page target", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		v := &Verifier{LookupCNAME: func(context.Context, string) (string, error) {
			return "spabc.cname.solidping.io.", nil
		}}

		diag := v.Diagnose(t.Context(), "status.acme.com", "spabc", "solidping.io", ModeToken)
		r.True(diag.OK)
		r.Contains(diag.String(), "mode=token")
		r.Contains(diag.String(), "expected=spabc.cname.solidping.io")
	})
}
