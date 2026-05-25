package discovery

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateCIDRs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cidrs   []string
		wantErr bool
		errCode bool // whether it wraps ErrRangeTooLarge
	}{
		{
			name:  "single host /32",
			cidrs: []string{"127.0.0.1/32"},
		},
		{
			name:  "small /24",
			cidrs: []string{"192.168.1.0/24"},
		},
		{
			name:  "/20 exactly at limit",
			cidrs: []string{"10.0.0.0/20"},
		},
		{
			name:    "empty cidrs",
			cidrs:   nil,
			wantErr: true,
		},
		{
			name:    "invalid CIDR",
			cidrs:   []string{"not-a-cidr"},
			wantErr: true,
		},
		{
			name:    "/8 too large",
			cidrs:   []string{"10.0.0.0/8"},
			wantErr: true,
			errCode: true,
		},
		{
			name:  "two /21 ranges at exactly 4096 limit",
			cidrs: []string{"10.0.0.0/21", "10.0.16.0/21"},
		},
		{
			name:    "two /20 ranges exceeding limit",
			cidrs:   []string{"10.0.0.0/20", "10.0.16.0/20"},
			wantErr: true,
			errCode: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			err := ValidateCIDRs(tc.cidrs)

			if tc.wantErr {
				r.Error(err)
				if tc.errCode {
					r.ErrorIs(err, ErrRangeTooLarge)
				}
			} else {
				r.NoError(err)
			}
		})
	}
}

func TestSplitCIDRs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cidrs       []string
		maxPerChunk int
		wantChunks  int
		wantErr     bool
	}{
		{
			name:        "single small cidr fits one chunk",
			cidrs:       []string{"192.168.1.0/24"},
			maxPerChunk: 4096,
			wantChunks:  1,
		},
		{
			name:        "/20 exactly one chunk",
			cidrs:       []string{"10.0.0.0/20"},
			maxPerChunk: 4096,
			wantChunks:  1,
		},
		{
			name:        "/18 splits into 4 chunks of /20",
			cidrs:       []string{"10.0.0.0/18"},
			maxPerChunk: 4096,
			wantChunks:  4,
		},
		{
			name:        "/16 splits into 16 chunks",
			cidrs:       []string{"10.0.0.0/16"},
			maxPerChunk: 4096,
			wantChunks:  16,
		},
		{
			name:        "two small cidrs packed into one chunk",
			cidrs:       []string{"192.168.1.0/25", "192.168.2.0/25"},
			maxPerChunk: 4096,
			wantChunks:  1,
		},
		{
			name:        "empty cidrs errors",
			cidrs:       nil,
			maxPerChunk: 4096,
			wantErr:     true,
		},
		{
			name:        "invalid cidr errors",
			cidrs:       []string{"nope"},
			maxPerChunk: 4096,
			wantErr:     true,
		},
		{
			name:        "ipv6 rejected",
			cidrs:       []string{"2001:db8::/120"},
			maxPerChunk: 4096,
			wantErr:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			chunks, err := SplitCIDRs(tc.cidrs, tc.maxPerChunk)

			if tc.wantErr {
				r.Error(err)

				return
			}

			r.NoError(err)
			r.Len(chunks, tc.wantChunks)

			// Every chunk must be valid for a bounded child scan.
			for _, chunk := range chunks {
				r.NoError(ValidateCIDRs(chunk))
			}
		})
	}
}

func TestValidatePlanCIDRs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cidrs    []string
		wantErr  bool
		tooLarge bool
	}{
		{
			name:  "small range ok",
			cidrs: []string{"192.168.1.0/24"},
		},
		{
			name:  "/18 ok (4 chunks)",
			cidrs: []string{"10.0.0.0/18"},
		},
		{
			name:  "/12 at ceiling (256 chunks)",
			cidrs: []string{"10.0.0.0/12"},
		},
		{
			name:     "/11 exceeds ceiling",
			cidrs:    []string{"10.0.0.0/11"},
			wantErr:  true,
			tooLarge: true,
		},
		{
			name:    "invalid cidr",
			cidrs:   []string{"bad"},
			wantErr: true,
		},
		{
			name:    "empty",
			cidrs:   nil,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			err := ValidatePlanCIDRs(tc.cidrs)

			if tc.wantErr {
				r.Error(err)
				if tc.tooLarge {
					r.ErrorIs(err, ErrRangeTooLarge)
				}
			} else {
				r.NoError(err)
			}
		})
	}
}

func TestExpandCIDR(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	ips, err := expandCIDR("127.0.0.1/32")
	r.NoError(err)
	r.Len(ips, 1)
	r.Equal("127.0.0.1", ips[0].String())

	ips, err = expandCIDR("192.168.1.0/30")
	r.NoError(err)
	r.Len(ips, 4)
}
