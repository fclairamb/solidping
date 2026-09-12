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

// sampleBearerChainSlug / sampleAggregateSlug identify the two samples
// promoted from the "Bearer-token login, then an authenticated call" and
// "Aggregating sub-checks" examples on the JavaScript checks doc page
// (web/docs/docs/features/javascript-checks.md). The docs_examples_test.go
// harness runs these SAME scripts against its httptest fixture, so the
// sample picker and the docs page cannot silently drift apart — a change to
// one that breaks the other fails the test suite, not a user's check.
const (
	sampleBearerChainSlug = "js-bearer-token-chain"
	sampleAggregateSlug   = "js-aggregate-subchecks"
)

// sampleBearerChainScript logs in with a JSON POST, then uses the returned
// bearer token on a second call — the multi-step API workflow the type's own
// "Complex multi-step API workflows" use-case bullet promises. The password
// comes from `secrets`, never `env`, matching the doc's own guidance on where
// a credential belongs.
const sampleBearerChainScript = `var login = http.post(env.BASE_URL + "/login", {
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ username: env.USERNAME, password: secrets.PASSWORD }),
});
if (login.error || login.statusCode !== 200) {
  return { status: "down", output: { step: "login", statusCode: login.statusCode, error: login.error } };
}
var token = JSON.parse(login.body).token;
var me = http.get(env.BASE_URL + "/me", {
  headers: { "Authorization": "Bearer " + token },
});
return { status: me.statusCode === 200 ? "up" : "down", output: { step: "me", statusCode: me.statusCode } };
`

// sampleAggregateScript folds three solidping.http() sub-checks into one
// result — the "Aggregating multiple checks into one" use case — reporting
// the worst status and each call's duration as a metric.
const sampleAggregateScript = `var checks = [
  solidping.http({ url: env.URL_1 }),
  solidping.http({ url: env.URL_2 }),
  solidping.http({ url: env.URL_3 }),
];
var worst = "up";
var metrics = {};
checks.forEach(function (result, index) {
  metrics["check" + index + "Duration"] = result.duration;
  if (result.status !== "up") {
    worst = "down";
  }
});
return { status: worst, metrics: metrics };
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
		{
			// Deliberately no `Secrets` entry here: a sample with a REAL value
			// in a map-shaped secret field is (as of this writing) the one
			// case that trips a shape mismatch in the server's dry-run
			// secret-placeholder injection (tracked separately — see
			// specs/todos/2026-09-12-02-secret-placeholder-must-match-the-fields-shape.md).
			// The script still reads `secrets.PASSWORD`, exactly like the doc
			// example; the dashboard `js` form already has its own `secrets`
			// editor (misc.tsx), so a user who picks this sample fills the
			// password in there directly — no API/CLI/config-as-code detour
			// required, just an empty field to fill in like any other blank
			// credential field.
			Name:   "JS: Bearer Token Login Chain",
			Slug:   sampleBearerChainSlug,
			Period: time.Minute * 5,
			Config: (&JSConfig{
				Script: sampleBearerChainScript,
				Env: map[string]string{
					"BASE_URL": "https://api.example.com",
					"USERNAME": "probe",
				},
			}).GetConfig(),
		},
		{
			Name:   "JS: Aggregate Sub-checks",
			Slug:   sampleAggregateSlug,
			Period: time.Minute * 5,
			Config: (&JSConfig{
				Script: sampleAggregateScript,
				Env: map[string]string{
					"URL_1": "https://api.example.com/health",
					"URL_2": "https://api.example.com/status",
					"URL_3": "https://api.example.com/ready",
				},
			}).GetConfig(),
		},
	}
}
