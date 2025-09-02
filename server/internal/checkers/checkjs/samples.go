package checkjs

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const sampleScript = `// Simple HTTP health check
var resp = http.get("https://httpbin.org/status/200");
if (resp.statusCode === 200) {
  return { status: "up", metrics: { statusCode: resp.statusCode, duration: resp.duration } };
}
return { status: "down", output: { error: "unexpected status: " + resp.statusCode } };
`

// GetSampleConfigs returns sample JavaScript check configurations.
func (c *JSChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "JS: HTTP Health Check",
			Slug:   "js-http-health",
			Period: time.Minute * 5,
			Config: (&JSConfig{
				Script: sampleScript,
			}).GetConfig(),
		},
	}
}
