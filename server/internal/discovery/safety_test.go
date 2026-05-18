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
