package domainverify

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

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

func TestRecords(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	records := Records("status.acme.com", "tok123", "cname.solidping.io")
	r.Len(records, 2)
	r.Equal("CNAME", records[0].Type)
	r.Equal("status.acme.com", records[0].Name)
	r.Equal("cname.solidping.io", records[0].Value)
	r.Equal("TXT", records[1].Type)
	r.Equal("_solidping-challenge.status.acme.com", records[1].Name)
	r.Equal("sp-domain-verify=tok123", records[1].Value)
}

func TestCheckTXT(t *testing.T) {
	t.Parallel()

	errLookup := errors.New("nxdomain")

	tests := []struct {
		name    string
		records []string
		lookErr error
		wantOK  bool
		wantErr bool
	}{
		{name: "match", records: []string{"sp-domain-verify=tok123"}, wantOK: true},
		{name: "match among many", records: []string{"other=1", "sp-domain-verify=tok123"}, wantOK: true},
		{name: "match with whitespace", records: []string{"  sp-domain-verify=tok123 "}, wantOK: true},
		{name: "wrong token", records: []string{"sp-domain-verify=nope"}, wantOK: false},
		{name: "missing record", records: []string{}, wantOK: false},
		{name: "lookup error", lookErr: errLookup, wantOK: false, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			v := &Verifier{
				LookupTXT: func(_ context.Context, name string) ([]string, error) {
					r.Equal("_solidping-challenge.status.acme.com", name)

					return tc.records, tc.lookErr
				},
				LookupCNAME: func(_ context.Context, _ string) (string, error) { return "", nil },
			}

			ok, err := v.CheckTXT(context.Background(), "status.acme.com", "tok123")
			r.Equal(tc.wantOK, ok)
			if tc.wantErr {
				r.Error(err)
			} else {
				r.NoError(err)
			}
		})
	}
}

func TestCheckCNAME(t *testing.T) {
	t.Parallel()

	errLookup := errors.New("nxdomain")

	tests := []struct {
		name    string
		cname   string
		lookErr error
		wantOK  bool
		wantErr bool
	}{
		{name: "exact", cname: "cname.solidping.io", wantOK: true},
		{name: "trailing dot", cname: "cname.solidping.io.", wantOK: true},
		{name: "case insensitive", cname: "CNAME.SolidPing.io", wantOK: true},
		{name: "wrong target", cname: "elsewhere.example.com", wantOK: false},
		{name: "lookup error", lookErr: errLookup, wantOK: false, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			v := &Verifier{
				LookupTXT: func(_ context.Context, _ string) ([]string, error) { return nil, nil },
				LookupCNAME: func(_ context.Context, host string) (string, error) {
					r.Equal("status.acme.com", host)

					return tc.cname, tc.lookErr
				},
			}

			ok, err := v.CheckCNAME(context.Background(), "status.acme.com", "cname.solidping.io")
			r.Equal(tc.wantOK, ok)
			if tc.wantErr {
				r.Error(err)
			} else {
				r.NoError(err)
			}
		})
	}
}

func TestVerify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		txt   []string
		cname string
		want  bool
	}{
		{name: "both pass", txt: []string{"sp-domain-verify=tok"}, cname: "cname.solidping.io", want: true},
		{name: "txt fail", txt: []string{"sp-domain-verify=bad"}, cname: "cname.solidping.io", want: false},
		{name: "cname fail", txt: []string{"sp-domain-verify=tok"}, cname: "other.example.com", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			v := &Verifier{
				LookupTXT:   func(_ context.Context, _ string) ([]string, error) { return tc.txt, nil },
				LookupCNAME: func(_ context.Context, _ string) (string, error) { return tc.cname, nil },
			}

			r.Equal(tc.want, v.Verify(context.Background(), "status.acme.com", "tok", "cname.solidping.io"))
		})
	}
}
