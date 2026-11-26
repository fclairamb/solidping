package checkworker

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestResultOutputMasksResolvedReferenceValues(t *testing.T) {
	t.Parallel()

	t.Run("dnsbl zones in values and map keys", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		zone := "acmekey123.zen.example.net"
		output := map[string]any{
			"clean":        []string{zone},
			"listed_on":    []string{},
			"inconclusive": []string{},
			"error_codes":  map[string][]string{zone: {"127.255.255.254"}},
			"target":       "192.0.2.10",
		}
		job := &models.CheckJob{ResolvedRefValues: []string{"acmekey123"}}

		got := resultOutput(job, output)

		blob, err := json.Marshal(got)
		r.NoError(err)
		r.NotContains(string(blob), "acmekey123")
		r.Equal([]any{"***.zen.example.net"}, got["clean"])
		r.Equal(map[string]any{"***.zen.example.net": []any{"127.255.255.254"}}, got["error_codes"])
		r.Equal("192.0.2.10", got["target"])

		// The checker's own map is left untouched.
		r.Equal([]string{zone}, output["clean"])
	})

	t.Run("output without the value is returned as is", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		output := map[string]any{"clean": []string{"zen.example.net"}, "count": 3}
		job := &models.CheckJob{ResolvedRefValues: []string{"acmekey123"}}

		got := resultOutput(job, output)

		r.Equal(output, got)
		r.IsType([]string{}, got["clean"], "no JSON round trip when nothing is masked")
	})

	t.Run("display URL and other fields together", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		job := &models.CheckJob{
			Config:            models.JSONMap{"url": "https://acme.example/?t=hunter2"},
			DisplayURL:        "https://acme.example/?t=${param:tok}",
			ResolvedRefValues: []string{"hunter2"},
		}
		output := map[string]any{
			"url":   "https://acme.example/?t=hunter2",
			"error": "GET https://acme.example/?t=hunter2: timeout",
		}

		got := resultOutput(job, output)

		r.Equal("https://acme.example/?t=${param:tok}", got["url"])
		r.Equal("GET https://acme.example/?t=***: timeout", got["error"])
	})

	t.Run("value needing JSON escaping", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		job := &models.CheckJob{ResolvedRefValues: []string{`p"a<ss`}}
		output := map[string]any{"message": `login with p"a<ss failed`}

		got := resultOutput(job, output)

		r.Equal("login with *** failed", got["message"])
	})

	t.Run("no reference value", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		output := map[string]any{"clean": []string{"zen.example.net"}}

		r.Equal(output, resultOutput(&models.CheckJob{}, output))
		r.Nil(resultOutput(&models.CheckJob{ResolvedRefValues: []string{"x"}}, nil))
	})

	t.Run("a value that is also a number never breaks the output", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		job := &models.CheckJob{ResolvedRefValues: []string{"8443", "acmekey123"}}
		output := map[string]any{
			"status_code": 8443,
			"ok":          true,
			"message":     "port 8443 answered for acmekey123.zen.example.net",
		}

		got := resultOutput(job, output)

		r.NotContains(got, "error", "the output is masked, not withheld")
		r.Equal(json.Number("8443"), got["status_code"], "numbers are never masked")
		r.Equal(true, got["ok"])
		r.Equal("port *** answered for ***.zen.example.net", got["message"])
	})

	t.Run("values shorter than the minimum are not masked", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		output := map[string]any{"message": "region eu answered on 443"}
		job := &models.CheckJob{ResolvedRefValues: []string{"eu", "443"}}

		r.Equal(output, resultOutput(job, output))
	})

	t.Run("the longest value is masked first", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		job := &models.CheckJob{ResolvedRefValues: []string{"acme", "acmekey123"}}

		got := resultOutput(job, map[string]any{"zone": "acmekey123.zen.example.net"})

		r.Equal("***.zen.example.net", got["zone"])
	})
}

func TestMaskMetricKeys(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	secrets := maskableValues([]string{"acmekey123"})
	metrics := map[string]any{"acmekey123.zen.example.net_ms": 12.5, "total_ms": 30}

	got := maskMetricKeys(metrics, secrets)

	r.Equal(map[string]any{"***.zen.example.net_ms": 12.5, "total_ms": 30}, got)
	r.Contains(metrics, "acmekey123.zen.example.net_ms", "the checker's map is left untouched")

	clean := map[string]any{"total_ms": 30}
	r.Equal(clean, maskMetricKeys(clean, secrets))
	r.Nil(maskMetricKeys(nil, secrets))
}

func TestMaskDiagnostics(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	secrets := maskableValues([]string{"hunter22"})
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

	got := maskDiagnostics(diagnostics, secrets)

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

	r.Nil(maskDiagnostics(nil, secrets))
	r.Same(diagnostics, maskDiagnostics(diagnostics, nil))
}
