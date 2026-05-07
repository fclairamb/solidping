package base_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

func TestParsePageLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		query   string
		def     int
		max     int
		want    int
		wantErr bool
	}{
		{name: "empty returns default", query: "", def: 50, max: 200, want: 50},
		{name: "valid limit", query: "limit=10", def: 50, max: 200, want: 10},
		{name: "limit clamped to max", query: "limit=999", def: 50, max: 200, want: 200},
		{name: "max=0 means no clamp", query: "limit=999", def: 50, max: 0, want: 999},
		{name: "size alias works", query: "size=15", def: 50, max: 200, want: 15},
		{name: "limit wins over size", query: "limit=10&size=99", def: 50, max: 200, want: 10},
		{name: "invalid integer rejected", query: "limit=abc", def: 50, max: 200, wantErr: true},
		{name: "zero rejected", query: "limit=0", def: 50, max: 200, wantErr: true},
		{name: "negative rejected", query: "limit=-1", def: 50, max: 200, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			vals, err := url.ParseQuery(tt.query)
			r.NoError(err)
			got, err := base.ParsePageLimit(vals, tt.def, tt.max)
			if tt.wantErr {
				r.Error(err)
				r.ErrorIs(err, base.ErrInvalidLimit)
				return
			}
			r.NoError(err)
			r.Equal(tt.want, got)
		})
	}
}
