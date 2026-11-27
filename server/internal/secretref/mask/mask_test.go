package mask

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestMaskMetricKeys(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	secrets := Values([]string{"acmekey123"})
	metrics := map[string]any{"acmekey123.zen.example.net_ms": 12.5, "total_ms": 30}

	got := MetricKeys(metrics, secrets)

	r.Equal(map[string]any{"***.zen.example.net_ms": 12.5, "total_ms": 30}, got)
	r.Contains(metrics, "acmekey123.zen.example.net_ms", "the checker's map is left untouched")

	clean := map[string]any{"total_ms": 30}
	r.Equal(clean, MetricKeys(clean, secrets))
	r.Nil(MetricKeys(nil, secrets))
}

func TestMaskDiagnostics(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	secrets := Values([]string{"hunter22"})
	diagnostics := &checkerdef.Diagnostics{
		FailureResponse: &checkerdef.FailureResponse{
			URL:        "https://acme.example/?t=hunter22",
			StatusLine: "HTTP/1.1 401 Unauthorized",
			StatusCode: 401,
			Headers:    map[string]string{"X-Echo-Token": "hunter22", "X-hunter22": "1"},
			Body:       `{"error":"bad token hunter22"}`,
		},
		NetworkFailure:  checkerdef.NewNetworkFailure("timeout", "hunter22.acme.example", "192.0.2.1:443", 443),
		ScreenshotError: "capture of hunter22 failed",
	}

	got := Diagnostics(diagnostics, secrets)

	blob, err := json.Marshal(got)
	r.NoError(err)
	r.NotContains(string(blob), "hunter22")
	r.Equal("https://acme.example/?t=***", got.FailureResponse.URL)
	r.Equal(map[string]string{"X-Echo-Token": "***", "X-***": "1"}, got.FailureResponse.Headers)
	r.Equal(401, got.FailureResponse.StatusCode)
	r.Equal("***.acme.example", got.NetworkFailure.Host)
	r.Equal("192.0.2.1:443", got.NetworkFailure.Address)

	// The checker's own struct is left untouched.
	r.Equal("https://acme.example/?t=hunter22", diagnostics.FailureResponse.URL)
	r.Equal("hunter22", diagnostics.FailureResponse.Headers["X-Echo-Token"])

	r.Nil(Diagnostics(nil, secrets))
	r.Same(diagnostics, Diagnostics(diagnostics, nil))
}
