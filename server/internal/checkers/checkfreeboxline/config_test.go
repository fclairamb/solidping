package checkfreeboxline_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkfreeboxline"
)

func TestConfig_FromMap_RoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	// Marshal then unmarshal to mimic the JSONB path: numeric fields
	// arrive as float64, mirroring what bun gives us.
	original := &checkfreeboxline.FreeboxLineConfig{
		ConnectionUID:       "conn-uid",
		LinkType:            "xdsl",
		MinSyncRateDownKbps: 80000,
		MinSnrMarginDownDb:  6,
		MaxAttenuationDb:    40,
		MaxCrcErrorsPerRun:  100,
	}

	raw, err := json.Marshal(original.GetConfig())
	r.NoError(err)

	var asMap map[string]any
	r.NoError(json.Unmarshal(raw, &asMap))

	parsed := &checkfreeboxline.FreeboxLineConfig{}
	r.NoError(parsed.FromMap(asMap))

	r.Equal(original.ConnectionUID, parsed.ConnectionUID)
	r.Equal(original.LinkType, parsed.LinkType)
	r.Equal(original.MinSyncRateDownKbps, parsed.MinSyncRateDownKbps)
	r.Equal(original.MinSnrMarginDownDb, parsed.MinSnrMarginDownDb)
	r.Equal(original.MaxAttenuationDb, parsed.MaxAttenuationDb)
	r.Equal(original.MaxCrcErrorsPerRun, parsed.MaxCrcErrorsPerRun)
}

func TestConfig_FromMap_FTTHFloats(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	parsed := &checkfreeboxline.FreeboxLineConfig{}
	r.NoError(parsed.FromMap(map[string]any{
		"connectionUid": "uid-1",
		"linkType":      "ftth",
		"minRxPowerMw":  0.05,
		"maxRxPowerMw":  0.5,
	}))

	r.Equal("uid-1", parsed.ConnectionUID)
	r.Equal("ftth", parsed.LinkType)
	r.InDelta(0.05, parsed.MinRxPowerMw, 0.0001)
	r.InDelta(0.5, parsed.MaxRxPowerMw, 0.0001)
}

func TestConfig_FromMap_BadType(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	parsed := &checkfreeboxline.FreeboxLineConfig{}
	err := parsed.FromMap(map[string]any{
		"connectionUid":       "uid-1",
		"linkType":            "xdsl",
		"minSyncRateDownKbps": "fast",
	})
	r.Error(err)
	r.Contains(err.Error(), "minSyncRateDownKbps")
}

func TestConfig_GetConfig_OmitsZero(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &checkfreeboxline.FreeboxLineConfig{
		ConnectionUID: "uid-1",
		LinkType:      "xdsl",
	}

	out := cfg.GetConfig()
	r.Equal("uid-1", out["connectionUid"])
	r.Equal("xdsl", out["linkType"])
	r.NotContains(out, "minSyncRateDownKbps")
	r.NotContains(out, "minRxPowerMw")
}

func TestConfig_SecretFields_Empty(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &checkfreeboxline.FreeboxLineConfig{}
	r.Empty(cfg.SecretFields())
}
