package checkhttp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFromMap_ExpectedStatusCamelCase(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &HTTPConfig{}
	r.NoError(cfg.FromMap(map[string]any{
		"url":            "http://example.com",
		"expectedStatus": 401,
	}))
	r.Equal(401, cfg.ExpectedStatus)
}

func TestFromMap_ExpectedStatusSnakeCaseLegacy(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &HTTPConfig{}
	r.NoError(cfg.FromMap(map[string]any{
		"url":             "http://example.com",
		"expected_status": 401,
	}))
	r.Equal(401, cfg.ExpectedStatus)
}

func TestFromMap_ExpectedStatusCodesCamelCase(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &HTTPConfig{}
	r.NoError(cfg.FromMap(map[string]any{
		"url":                 "http://example.com",
		"expectedStatusCodes": []any{"401", "4XX"},
	}))
	r.Equal([]string{"401", "4XX"}, cfg.ExpectedStatusCodes)
}

func TestFromMap_BodyKeysCamelCase(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &HTTPConfig{}
	r.NoError(cfg.FromMap(map[string]any{
		"url":               "http://example.com",
		"bodyExpect":        "ok",
		"bodyReject":        "error",
		"bodyPattern":       "^OK",
		"bodyPatternReject": "FAIL",
		"headersPattern":    map[string]any{"X-OK": "yes"},
	}))
	r.Equal("ok", cfg.BodyExpect)
	r.Equal("error", cfg.BodyReject)
	r.Equal("^OK", cfg.BodyPattern)
	r.Equal("FAIL", cfg.BodyPatternReject)
	r.Equal(map[string]string{"X-OK": "yes"}, cfg.HeadersPattern)
}

func TestGetConfig_WritesCamelCase(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &HTTPConfig{
		URL:                 "http://example.com",
		ExpectedStatus:      401,
		ExpectedStatusCodes: []string{"4XX"},
		BodyExpect:          "ok",
	}

	out := cfg.GetConfig()
	r.Equal(401, out["expectedStatus"])
	r.Equal([]string{"4XX"}, out["expectedStatusCodes"])
	r.Equal("ok", out["bodyExpect"])
	r.NotContains(out, "expected_status")
	r.NotContains(out, "expected_status_codes")
}
