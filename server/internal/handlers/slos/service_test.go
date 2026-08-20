package slos

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSlugify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{in: "API availability", want: "api-availability"},
		{in: "  Edge   /  CDN  ", want: "edge-cdn"},
		{in: "99.9% monthly", want: "99-9-monthly"},
		{in: "acme", want: "acme"},
		{in: "---", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, Slugify(tt.in))
		})
	}
}

// normalizeCreateRequest is where every scalar default and range rule lives, so
// it is table-tested directly rather than only through the HTTP surface.
func TestNormalizeCreateRequest(t *testing.T) {
	t.Parallel()

	target := func(v float64) *float64 { return &v }

	tests := []struct {
		name         string
		req          CreateRequest
		wantErr      error
		wantSlug     string
		wantTarget   float64
		wantTimezone string
	}{
		{
			name:         "defaults",
			req:          CreateRequest{Name: "API availability"},
			wantSlug:     "api-availability",
			wantTarget:   99.9,
			wantTimezone: "UTC",
		},
		{
			name: "explicit values survive",
			req: CreateRequest{
				Name: "API", Slug: "api", TargetPct: target(99.95), Timezone: "Europe/Paris",
			},
			wantSlug: "api", wantTarget: 99.95, wantTimezone: "Europe/Paris",
		},
		{
			name:    "name is required",
			req:     CreateRequest{},
			wantErr: ErrNameRequired,
		},
		{
			name:    "slug must be url-safe",
			req:     CreateRequest{Name: "API", Slug: "Not A Slug"},
			wantErr: ErrInvalidSlug,
		},
		{
			// A name with nothing slug-able in it cannot silently produce an
			// empty slug — that would collide with every other such name.
			name:    "unslugifiable name is rejected",
			req:     CreateRequest{Name: "***"},
			wantErr: ErrInvalidSlug,
		},
		{
			name:    "zero target is rejected",
			req:     CreateRequest{Name: "API", TargetPct: target(0)},
			wantErr: ErrInvalidTarget,
		},
		{
			name:    "negative target is rejected",
			req:     CreateRequest{Name: "API", TargetPct: target(-1)},
			wantErr: ErrInvalidTarget,
		},
		{
			name:    "target above 100 is rejected",
			req:     CreateRequest{Name: "API", TargetPct: target(100.5)},
			wantErr: ErrInvalidTarget,
		},
		{
			// 100% is a legitimate (if brutal) objective: zero allowance.
			name:       "exactly 100 is allowed",
			req:        CreateRequest{Name: "API", TargetPct: target(100)},
			wantSlug:   "api",
			wantTarget: 100, wantTimezone: "UTC",
		},
		{
			name:    "unknown timezone is rejected",
			req:     CreateRequest{Name: "API", Timezone: "Mars/Olympus_Mons"},
			wantErr: ErrInvalidTimezone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			req := tt.req

			slug, target, timezone, err := normalizeCreateRequest(&req)

			if tt.wantErr != nil {
				r.ErrorIs(err, tt.wantErr)

				return
			}

			r.NoError(err)
			r.Equal(tt.wantSlug, slug)
			r.InDelta(tt.wantTarget, target, 0.0001)
			r.Equal(tt.wantTimezone, timezone)
		})
	}
}
