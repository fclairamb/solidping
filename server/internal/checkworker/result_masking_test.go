package checkworker

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

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
}
