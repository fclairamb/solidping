package aichecks

import (
	"fmt"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkjs"
)

// jsAPIReference is the condensed js check API the model writes against. It
// mirrors web/docs/docs/features/javascript-checks.md; the samples appended
// by systemPrompt are the same scripts the docs harness executes.
const jsAPIReference = `# SolidPing js check API

A js check is a JavaScript (ES5.1 + most of ES2015, goja engine) function body.
It runs top to bottom, synchronously, and must ` + "`return`" + ` an object:

  return { status: "up" | "down" | "error", metrics: { name: number }, output: { ... } };

Never return "timeout". A throw, a missing return or an unknown status is "error".
There is no setTimeout, fetch, require, async/await or Promise: every call blocks.

Globals:
- console.log/info/warn/error(...): appended to output.console (16 KB cap).
- sleep(ms): the only way to wait.
- env.NAME: plaintext parameters. secrets.NAME: encrypted parameters (passwords, tokens).
- base64.encode(text) / base64.decode(text).
- http.get/post/put/patch/delete/head(url, { body, headers, followRedirects, maxRedirects,
  redirectHostPolicy: "any" | "same-host", timeout }) returns
  { statusCode, body (1 MB cap), headers (canonical keys), url, redirects, duration }
  or { error } when the request could not be made. Always check resp.error first.
- http.session(): same verbs plus cookies(url), sharing one cookie jar.
- browser.open() returns a real headless Chrome page (one per run, throws without Chrome):
  page.goto(url) -> { ok, url, title, duration, error? }
  page.waitFor(selector, { timeout? }) -> { ok, duration, error? }
  page.click(selector) / page.fill(selector, text) / page.press(selector, key) -> { ok, error? }
  page.text(selector) -> { ok, text, error? }
  click, fill, press and text wait for their selector until the whole run budget is
  spent: call page.waitFor(selector, { timeout: "5s" }) first, so a missing element
  fails in 5 s instead of timing the run out.
  page.evaluate(expression) -> { ok, value, error? } (runs inside the page)
  page.url() -> string, page.cookies() -> [...], page.close()
  page.screenshot() -> { ok, error? }: captures the visible 1280x800 viewport and attaches
  it to the result (the check page's Screenshots card). The last call wins. It is kept
  on "down"/"timeout" runs and on "Capture now" runs, dropped on other "up" runs.
- tcp.connect(host, port, opts) / udp / websocket.connect(url) sockets, rdp, vnc:
  only when the user asks for those protocols.
- solidping.check(type, config) / solidping.http(config): run another check type.
At most 20 http/solidping calls per run. The whole run has a 30 s budget.
`

// scriptRules are the rules every generated or repaired script follows.
const scriptRules = `# Rules for the script you write

1. Implement the contract: one explicit check per contract line, in order.
2. Tag every failure: when an assertion of the contract fails because the
   service misbehaves, return { status: "down", output: { failure: "assertion", step: "...", ... } }.
   When the script cannot find what it expected to drive (a selector, a field,
   a JSON key, a page structure), return { status: "down", output: { failure: "drift", step: "...", ... } }.
3. Never wrap an assertion in try/catch, never widen a match to make it pass,
   never remove an assertion. A failing check is what a monitor is for.
4. Credentials always come from secrets.NAME, never literals. Plain parameters
   (base URL, user name) come from env.NAME when they are provided.
5. Only contact the hosts the user named.
6. Keep it short, readable and deterministic. Report useful metrics (durations).
7. When the user asks for a screenshot, call page.screenshot() at the moment
   they describe (e.g. after filling a form), and again just before returning
   "down" so a failure shows the page. A screenshot is never an assertion: do
   not fail the check because page.screenshot() returned ok: false.
8. You may use run_script with final: false to probe the target (list headings,
   try a selector). A probe is never saved. Test the finished script, the one that
   implements every contract line, with final: true. When a final run returns
   "up", stop and answer with that script in a single fenced js code block.
9. When a run shows the target itself fails the contract (credentials rejected,
   a 4xx/5xx answer, the data missing), a correct script returns "down" and can
   never pass the test. Do not keep rewriting it: answer in one or two sentences
   what fails and what the user should fix, without calling a tool.
`

// systemPrompt is the stable system prompt of every authoring call: the API
// reference, the samples and the rules. Stable on purpose, so the provider's
// prompt cache serves it.
func systemPrompt() string {
	var builder strings.Builder

	builder.WriteString("You write SolidPing `js` monitoring check scripts.\n\n")
	builder.WriteString(jsAPIReference)
	builder.WriteString("\n# Examples\n")

	samples := (&checkjs.JSChecker{}).GetSampleConfigs(&checkerdef.ListSampleOptions{})
	for i := range samples {
		script, _ := samples[i].Config["script"].(string)
		if script == "" {
			continue
		}

		fmt.Fprintf(&builder, "\n## %s\n```js\n%s\n```\n", samples[i].Name, strings.TrimSpace(script))
	}

	builder.WriteString("\n")
	builder.WriteString(scriptRules)

	return builder.String()
}

// contractSystemPrompt asks for the contract only.
const contractSystemPrompt = `You turn a description of what a user wants to monitor into a contract:
an ordered list of short, human-readable, verifiable assertions a monitoring
script will check, one per line, in the order they happen (e.g.
"GET https://acme.com/login answers 200", "logging in with the test account
succeeds", "the dashboard lists at least one project").
Answer with a JSON array of strings and nothing else.`
