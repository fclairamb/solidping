package checks

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFormatDurationSecondsCompact pins the rendering rule: the largest single
// unit (h, m, s) that divides evenly.
func TestFormatDurationSecondsCompact(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cases := map[int]string{
		0:     "0s",
		30:    "30s",
		60:    "1m",
		90:    "90s",
		120:   "2m",
		3600:  "1h",
		21600: "6h",
		3660:  "61m",
		86400: "24h",
	}
	for secs, want := range cases {
		r.Equal(want, formatDurationSecondsCompact(secs), "seconds=%d", secs)
	}
}

// TestDurationStringToSeconds covers the accepted input dialects: Go-style,
// HH:MM:SS (v1 period), and ISO8601.
func TestDurationStringToSeconds(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cases := map[string]int{
		"30s":      30,
		"2m":       120,
		"6h":       21600,
		"1m30s":    90,
		"00:00:30": 30,
		"06:00:00": 21600,
		"PT1M":     60,
	}
	for in, want := range cases {
		got, err := durationStringToSeconds(in)
		r.NoError(err, "input=%s", in)
		r.Equal(want, got, "input=%s", in)
	}

	_, err := durationStringToSeconds("nonsense")
	r.Error(err)
}

// TestModalInt picks the most frequent value and breaks ties toward the smaller.
func TestModalInt(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(2, modalInt([]int{2, 2, 5}))
	r.Equal(8, modalInt([]int{8}))
	// Tie between 2 and 5 (one each): smaller wins.
	r.Equal(2, modalInt([]int{5, 2}))
	r.Equal(0, modalInt([]int{}))
}

// TestModalRegions picks the most frequent regions list deterministically.
func TestModalRegions(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	got := modalRegions([][]string{
		{"default"},
		{"default"},
		{"default", "eu-2"},
	})
	r.Equal([]string{"default"}, got)
}

// TestBuildExportDocumentV2 verifies the defaults block is the modal, common
// checks carry no overrides, and a deviating check emits only its overrides.
func TestBuildExportDocumentV2(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	common := func(slug string) ExportCheck {
		return ExportCheck{
			Name:                      slug,
			Slug:                      slug,
			Type:                      "http",
			Config:                    map[string]any{"url": "https://example.com/" + slug},
			Regions:                   []string{"default"},
			Enabled:                   true,
			Period:                    "00:01:00",
			ConfirmationPeriodSeconds: intPtr(120),
			EscalationThreshold:       intPtr(10),
			RecoveryPeriodSeconds:     intPtr(120),
			FlappingWindowSeconds:     intPtr(21600),
			FlapBackoffFactor:         intPtr(2),
			MaxRecoveryMultiplier:     intPtr(8),
		}
	}

	dnsbl := common("dnsbl")
	dnsbl.Period = "01:00:00"
	dnsbl.Regions = []string{"default", "eu-2"}
	dnsbl.FlapBackoffFactor = intPtr(5)
	dnsbl.MaxRecoveryMultiplier = intPtr(10)
	dnsbl.ReopenCooldownMultiplier = intPtr(5)
	dnsbl.Enabled = false

	doc := &ExportDocument{
		Version:      ExportVersionV2,
		Organization: "acme",
		Secrets:      SecretsMarkerStripped,
		Checks:       []ExportCheck{common("a"), common("b"), dnsbl},
	}

	wire, err := buildExportDocumentV2(doc)
	r.NoError(err)

	// Defaults block reflects the modal (the two common checks win every field).
	r.NotNil(wire.Defaults)
	r.Equal([]string{"default"}, wire.Defaults.Regions)
	r.Equal("1m", wire.Defaults.Period)
	r.Equal("2m", wire.Defaults.ConfirmationPeriod)
	r.Equal(10, *wire.Defaults.EscalationThreshold)
	r.Equal("2m", wire.Defaults.RecoveryPeriod)
	r.Equal("6h", wire.Defaults.FlappingWindow)
	r.Equal(2, *wire.Defaults.FlapBackoffFactor)
	r.Equal(8, *wire.Defaults.MaxRecoveryMultiplier)

	// Common checks carry no overrides and are not disabled.
	a := wire.Checks[0]
	r.Equal("a", a.Slug)
	r.False(a.Disabled)
	r.Empty(a.Regions)
	r.Empty(a.Period)
	r.Empty(a.ConfirmationPeriod)
	r.Nil(a.FlapBackoffFactor)
	r.Nil(a.ReopenCooldownMultiplier)

	// The deviating check emits only its overrides.
	d := wire.Checks[2]
	r.Equal("dnsbl", d.Slug)
	r.True(d.Disabled)
	r.Equal([]string{"default", "eu-2"}, d.Regions)
	r.Equal("1h", d.Period)
	r.Equal(5, *d.FlapBackoffFactor)
	r.Equal(10, *d.MaxRecoveryMultiplier)
	r.Equal(5, *d.ReopenCooldownMultiplier)
	// Fields equal to the default stay omitted even on the deviating check.
	r.Empty(d.ConfirmationPeriod)
	r.Nil(d.EscalationThreshold)
}

// TestUnmarshalV2ResolvesDefaults verifies decode resolves each check field
// against the defaults block (check value beats default) and that an absent
// field with no default resolves to nil (system default).
func TestUnmarshalV2ResolvesDefaults(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	body := []byte(`{
      "version": 2,
      "organization": "acme",
      "secrets": "stripped",
      "defaults": {
        "regions": ["default"],
        "period": "1m",
        "confirmationPeriod": "2m",
        "flapBackoffFactor": 2
      },
      "checks": [
        {"name": "a", "slug": "a", "type": "http", "config": {"url": "https://a"}},
        {"name": "b", "slug": "b", "type": "http", "config": {"url": "https://b"},
         "period": "1h", "flapBackoffFactor": 5, "disabled": true}
      ]
    }`)

	var doc ExportDocument
	r.NoError(json.Unmarshal(body, &doc))
	r.Equal(2, doc.Version)
	r.Equal(SecretsMarkerStripped, doc.Secrets)
	r.Len(doc.Checks, 2)

	// Check "a" inherits the document defaults.
	a := doc.Checks[0]
	r.True(a.Enabled)
	r.Equal([]string{"default"}, a.Regions)
	r.Equal("1m", a.Period)
	r.NotNil(a.ConfirmationPeriodSeconds)
	r.Equal(120, *a.ConfirmationPeriodSeconds)
	r.NotNil(a.FlapBackoffFactor)
	r.Equal(2, *a.FlapBackoffFactor)
	// No default and not set on the check → nil (system default at create).
	r.Nil(a.RecoveryPeriodSeconds)
	r.Nil(a.MaxRecoveryMultiplier)

	// Check "b" overrides beat the defaults.
	b := doc.Checks[1]
	r.False(b.Enabled)
	r.Equal("1h", b.Period)
	r.Equal(5, *b.FlapBackoffFactor)
	// confirmationPeriod not set on b → inherits the "2m" default.
	r.Equal(120, *b.ConfirmationPeriodSeconds)
}
