package checks

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestConfigEqual pins the type-awareness of configEqual (spec 2026-08-29-09).
//
// The old implementation compared values with fmt.Sprintf("%v", …), which
// renders []any{float64(200), float64(403)} and []any{"200", "403"} as the
// same string "[200 403]". Every "unequal" case below whose values only differ
// by TYPE is a case the old comparison got wrong, silently stranding the
// denormalized check_jobs.config on the previous snapshot.
//
// The "equal" cases are the positive control: a comparison that always
// returned false would fix the regression and make every reconcile rewrite
// every job row, so both halves have to hold.
func TestConfigEqual(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		configA models.JSONMap
		configB models.JSONMap
		want    bool
	}{
		{
			name:    "numeric vs string status codes (the production regression)",
			configA: models.JSONMap{"expectedStatusCodes": []any{float64(200), float64(403)}},
			configB: models.JSONMap{"expectedStatusCodes": []any{"200", "403"}},
			want:    false,
		},
		{
			name:    "scalar number vs its string form",
			configA: models.JSONMap{"expectedStatus": float64(200)},
			configB: models.JSONMap{"expectedStatus": "200"},
			want:    false,
		},
		{
			name:    "bool vs its string form",
			configA: models.JSONMap{"followRedirects": true},
			configB: models.JSONMap{"followRedirects": "true"},
			want:    false,
		},
		{
			name:    "type difference nested inside a map",
			configA: models.JSONMap{"tls": map[string]any{"minVersion": float64(12)}},
			configB: models.JSONMap{"tls": map[string]any{"minVersion": "12"}},
			want:    false,
		},
		{
			name:    "type difference nested inside a slice of maps",
			configA: models.JSONMap{"steps": []any{map[string]any{"code": float64(200)}}},
			configB: models.JSONMap{"steps": []any{map[string]any{"code": "200"}}},
			want:    false,
		},
		{
			name:    "same elements in a different order are not the same array",
			configA: models.JSONMap{"expectedStatusCodes": []any{"200", "403"}},
			configB: models.JSONMap{"expectedStatusCodes": []any{"403", "200"}},
			want:    false,
		},
		{
			name:    "null vs the string null",
			configA: models.JSONMap{"body": nil},
			configB: models.JSONMap{"body": "null"},
			want:    false,
		},
		{
			name:    "identical config compares equal (no-op edits stay cheap)",
			configA: models.JSONMap{"url": "https://acme.com", "expectedStatusCodes": []any{"200", "403"}},
			configB: models.JSONMap{"url": "https://acme.com", "expectedStatusCodes": []any{"200", "403"}},
			want:    true,
		},
		{
			name:    "identical nested config compares equal",
			configA: models.JSONMap{"headers": map[string]any{"X-Acme": "1"}, "timeout": float64(30)},
			configB: models.JSONMap{"headers": map[string]any{"X-Acme": "1"}, "timeout": float64(30)},
			want:    true,
		},
		{
			name: "Go int and JSON-decoded float64 of the same number are the same config",
			// The check side can hold an int straight out of a request body
			// decoded into a Go literal, the job side a float64 out of the DB.
			// Both serialize to 200, so this must NOT trigger a rewrite.
			configA: models.JSONMap{"timeout": 30},
			configB: models.JSONMap{"timeout": float64(30)},
			want:    true,
		},
		{
			name:    "different value for the same key",
			configA: models.JSONMap{"url": "https://acme.com"},
			configB: models.JSONMap{"url": "https://status.acme.com"},
			want:    false,
		},
		{
			name:    "different number of keys",
			configA: models.JSONMap{"url": "https://acme.com"},
			configB: models.JSONMap{"url": "https://acme.com", "method": "POST"},
			want:    false,
		},
		{
			name:    "same key count, different key names",
			configA: models.JSONMap{"url": "https://acme.com"},
			configB: models.JSONMap{"uri": "https://acme.com"},
			want:    false,
		},
		{
			name:    "both nil",
			configA: nil,
			configB: nil,
			want:    true,
		},
		{
			name:    "nil and empty are both no config at all",
			configA: nil,
			configB: models.JSONMap{},
			want:    true,
		},
		{
			name:    "nil and a populated config",
			configA: nil,
			configB: models.JSONMap{"url": "https://acme.com"},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			r.Equal(tt.want, configEqual(tt.configA, tt.configB))
			// Equality must be symmetric — the caller passes (job, check) but
			// nothing about the fix should depend on that order.
			r.Equal(tt.want, configEqual(tt.configB, tt.configA))
		})
	}
}

// TestConfigEqualKeyOrderIsNotADifference confirms — rather than assumes —
// that encoding/json sorts map keys, so two identical configs whose Go maps
// were populated in opposite orders still compare equal. Without that
// guarantee the canonical-JSON comparison would report a difference on every
// reconcile and rewrite every job row forever.
//
// Go randomizes map iteration order per range, so the loop makes the "they
// happened to iterate the same way" coincidence vanishingly unlikely.
func TestConfigEqualKeyOrderIsNotADifference(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	keys := []string{"url", "method", "timeout", "expectedStatusCodes", "headers", "body", "zzz", "aaa"}

	for range 200 {
		forward := models.JSONMap{}
		backward := models.JSONMap{}

		for i, key := range keys {
			forward[key] = i
		}

		for i := len(keys) - 1; i >= 0; i-- {
			backward[keys[i]] = i
		}

		r.True(configEqual(forward, backward), "insertion order must not make two identical configs differ")
	}
}
