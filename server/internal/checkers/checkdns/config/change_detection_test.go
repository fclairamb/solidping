package config

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// jsonRoundTrip mimics a config read back from the database: every nested
// value decoded as map[string]any / []any.
func jsonRoundTrip(t *testing.T, in map[string]any) map[string]any {
	t.Helper()

	raw, err := json.Marshal(in)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))

	return out
}

func TestChangeDetection_RoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &DNSConfig{
		Host:          "acme.com",
		RecordType:    RecordTypeNS,
		DetectChanges: true,
		OnChange:      OnChangeWarning,
		Baseline: map[string][]string{
			"eu-west": {"ns1.acme.com", "ns2.acme.com"},
			"us-east": {"ns1.acme.com"},
		},
	}

	again := &DNSConfig{}
	r.NoError(again.FromMap(jsonRoundTrip(t, cfg.GetConfig())))
	r.True(again.DetectChanges)
	r.Equal(OnChangeWarning, again.OnChange)
	r.Equal(cfg.Baseline, again.Baseline)
}

func TestChangeDetection_GetConfigOmitsDefaults(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	out := (&DNSConfig{Host: "acme.com", OnChange: OnChangeDown, Baseline: map[string][]string{}}).GetConfig()
	r.NotContains(out, keyDetectChanges)
	r.NotContains(out, keyBaseline)
	r.NotContains(out, keyOnChange)
}

func TestChangeDetection_FromMapRejectsBadTypes(t *testing.T) {
	t.Parallel()

	for name, cfg := range map[string]map[string]any{
		"detect_changes not bool": {"host": "acme.com", keyDetectChanges: "yes"},
		"on_change not string":    {"host": "acme.com", keyOnChange: 1},
		"baseline not object":     {"host": "acme.com", keyBaseline: []any{"x"}},
		"baseline values bad":     {"host": "acme.com", keyBaseline: map[string]any{"eu": []any{1}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Error(t, (&DNSConfig{}).FromMap(cfg))
		})
	}
}

func TestChangeDetection_Validate(t *testing.T) {
	t.Parallel()

	tooMany := make([]any, MaxBaselineValues+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("10.0.0.%d", i)
	}

	tests := []struct {
		name    string
		config  map[string]any
		wantErr string
	}{
		{
			name:   "valid",
			config: map[string]any{"host": "acme.com", keyDetectChanges: true, keyOnChange: "warning"},
		},
		{
			name: "valid with baseline",
			config: map[string]any{
				"host": "acme.com", keyDetectChanges: true,
				keyBaseline: map[string]any{"eu-west": []any{"1.2.3.4"}},
			},
		},
		{
			name:    "unknown on_change",
			config:  map[string]any{"host": "acme.com", keyDetectChanges: true, keyOnChange: "panic"},
			wantErr: "on_change",
		},
		{
			name: "baseline without detect_changes",
			config: map[string]any{
				"host": "acme.com", keyBaseline: map[string]any{"eu-west": []any{"1.2.3.4"}},
			},
			wantErr: "requires detect_changes",
		},
		{
			name:   "empty baseline without detect_changes is fine",
			config: map[string]any{"host": "acme.com", keyBaseline: map[string]any{}},
		},
		{
			name: "too many values",
			config: map[string]any{
				"host": "acme.com", keyDetectChanges: true, keyBaseline: map[string]any{"eu-west": tooMany},
			},
			wantErr: "at most 100",
		},
		{
			name: "empty region key",
			config: map[string]any{
				"host": "acme.com", keyDetectChanges: true, keyBaseline: map[string]any{" ": []any{"1.2.3.4"}},
			},
			wantErr: "region keys",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateSpec(&checkerdef.CheckSpec{Config: tt.config})
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestSelectRegion_PicksRegionBaseline(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &DNSConfig{Baseline: map[string][]string{
		"eu-west":          {"a"},
		"us-east":          {"b"},
		DefaultBaselineKey: {"c"},
	}}

	cfg.SelectRegion("us-east")
	values, ok := cfg.RegionBaseline()
	r.True(ok)
	r.Equal([]string{"b"}, values)

	cfg.SelectRegion("ap-south")
	_, ok = cfg.RegionBaseline()
	r.False(ok, "a region absent from the map has no baseline")

	cfg.SelectRegion("")
	values, ok = cfg.RegionBaseline()
	r.True(ok)
	r.Equal([]string{"c"}, values)
}

func TestPreserveAbsentFields(t *testing.T) {
	t.Parallel()

	stored := map[string]any{
		"host": "acme.com", keyDetectChanges: true,
		keyBaseline: map[string]any{"eu-west": []any{"1.2.3.4"}},
	}

	t.Run("omitted baseline is kept", func(t *testing.T) {
		t.Parallel()

		merged := map[string]any{"host": "acme.com", keyDetectChanges: true}
		(&DNSConfig{}).PreserveAbsentFields(stored, merged)
		require.Equal(t, stored[keyBaseline], merged[keyBaseline])
	})

	t.Run("explicit empty baseline clears", func(t *testing.T) {
		t.Parallel()

		merged := map[string]any{"host": "acme.com", keyDetectChanges: true, keyBaseline: map[string]any{}}
		(&DNSConfig{}).PreserveAbsentFields(stored, merged)
		require.Equal(t, map[string]any{}, merged[keyBaseline])
	})

	t.Run("detection turned off drops it", func(t *testing.T) {
		t.Parallel()

		merged := map[string]any{"host": "acme.com"}
		(&DNSConfig{}).PreserveAbsentFields(stored, merged)
		require.NotContains(t, merged, keyBaseline)
	})
}

func TestNormalizeValues(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal([]string{"ns1.acme.com", "ns2.acme.com"},
		NormalizeValues(RecordTypeNS, []string{" NS2.acme.com. ", "ns1.acme.com", "ns1.ACME.com."}))
	r.Equal([]string{"10 mx1.acme.com", "20 mx1.acme.com"},
		NormalizeValues(RecordTypeMX, []string{"20 mx1.acme.com", "10 MX1.acme.com."}))
	r.Equal([]string{"Verify=AbC", "v=spf1 -all"},
		NormalizeValues(RecordTypeTXT, []string{"v=spf1 -all", "Verify=AbC", "Verify=AbC"}))
}
