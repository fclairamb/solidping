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
// sampleBrowserLoginSlug is the third promoted example: the "Form login
// through a real browser" section. Its script is the SAME text as the doc
// fence tagged `<!-- test: browser-login -->`, asserted character for
// character by docs_examples_test.go — the sample picker and the docs page
// cannot drift apart.
const (
	sampleBearerChainSlug  = "js-bearer-token-chain"
	sampleAggregateSlug    = "js-aggregate-subchecks"
	sampleBrowserLoginSlug = "js-browser-login"
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

// sampleBrowserLoginScript drives a real headless-Chrome page: fill the login
// form, wait for the app to render, then hand the browser's session to a plain
// HTTP call. It is the workflow neither check type could express alone (spec
// 2026-09-12-06) and the one the `browser` global exists for.
//
// The sample's period is 1 minute, not the 5 the other samples use: a script
// that opens a browser inherits the browser check's floor, so anything faster
// would be a sample the server refuses to save.
const sampleBrowserLoginScript = `var page = browser.open();
var nav = page.goto(env.BASE_URL + "/login");
if (!nav.ok) return { status: "down", output: { step: "load", error: nav.error } };
page.fill("#email", env.USERNAME);
page.fill("#password", secrets.PASSWORD);
page.click("button[type=submit]");
var dash = page.waitFor("[data-testid=dashboard]", { timeout: "10s" });
if (!dash.ok) {
  page.screenshot();
  return { status: "down", output: { step: "login", url: page.url(), error: dash.error } };
}
// Hand the browser's session to a plain HTTP call: cookies go on the header,
// http.session()'s jar is filled by the target only.
var cookie = page.cookies().map(function (c) { return c.name + "=" + c.value; }).join("; ");
var me = http.get(env.BASE_URL + "/api/me", { headers: { Cookie: cookie } });
if (me.error || me.statusCode !== 200) {
  return { status: "down", output: { step: "api", statusCode: me.statusCode, error: me.error } };
}
return { status: "up", metrics: { loginMs: nav.duration + dash.duration } };
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
			// Like the bearer-chain sample above, deliberately no `Secrets`
			// entry: the script reads secrets.PASSWORD and the dashboard's
			// `js` form has its own secrets editor to fill it in.
			Name:   "JS: Browser Form Login",
			Slug:   sampleBrowserLoginSlug,
			Period: time.Minute,
			Config: (&JSConfig{
				Script: sampleBrowserLoginScript,
				Env: map[string]string{
					"BASE_URL": "https://app.example.com",
					"USERNAME": "probe@example.com",
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
