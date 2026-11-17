package importers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// Pingdom TMS (transaction) check kinds.
const (
	pingdomTMSScript    = "script"
	pingdomTMSRecording = "recording"
)

const (
	// pingdomTMSPageSize is the page size requested from GET /tms/check.
	pingdomTMSPageSize = 100
	// pingdomTMSMaxPages bounds pagination against a misbehaving API.
	pingdomTMSMaxPages = 50
	// pingdomMaxSleep bounds a generated wait (the JS check budget is 30s).
	pingdomMaxSleepSeconds = 30
	// pingdomDefaultWaitSeconds is how long wait_for_contains polls.
	pingdomDefaultWaitSeconds = 10
	// pingdomSecretUser and pingdomSecretPassword are the encrypted secrets
	// holding a basic_auth step's credentials.
	pingdomSecretUser     = "PINGDOM_BASIC_USER"
	pingdomSecretPassword = "PINGDOM_BASIC_PASSWORD"
)

// errPingdomNotFound marks an HTTP 404 from the API.
var errPingdomNotFound = errors.New("not found")

// pingdomTMSCheck is the subset of a Pingdom TMS check (API 3.1 CheckWithoutIDGET)
// we map.
//
//nolint:tagliatelle // field names are Pingdom's, not ours
type pingdomTMSCheck struct {
	ID                       int64             `json:"id"`
	Name                     string            `json:"name"`
	Active                   *bool             `json:"active"`
	Interval                 int               `json:"interval"`
	Region                   string            `json:"region"`
	Type                     string            `json:"type"`
	Tags                     []json.RawMessage `json:"tags"`
	Steps                    []pingdomTMSStep  `json:"steps"`
	SendNotificationWhenDown int               `json:"send_notification_when_down"`
	Metadata                 struct {
		Authentications struct {
			HTTPAuthentications []struct {
				Host        string `json:"host"`
				Credentials struct {
					UserName string `json:"userName"`
					Password string `json:"password"`
				} `json:"credentials"`
			} `json:"httpAuthentications"`
		} `json:"authentications"`
	} `json:"metadata"`
}

// pingdomTMSStep is one step: a function name and its arguments.
type pingdomTMSStep struct {
	Fn   string         `json:"fn"`
	Args map[string]any `json:"args"`
}

// arg returns a step argument as a string; numbers are rendered without
// trailing zeros, anything else is empty.
func (s pingdomTMSStep) arg(key string) string {
	switch value := s.Args[key].(type) {
	case string:
		return value
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(value)
	default:
		return ""
	}
}

// isPingdomTMS reports whether a raw item is a transaction check.
func isPingdomTMS(raw json.RawMessage) bool {
	var probe struct {
		Type  json.RawMessage `json:"type"`
		Steps json.RawMessage `json:"steps"`
	}

	if json.Unmarshal(raw, &probe) != nil {
		return false
	}

	if len(probe.Steps) > 0 && !bytes.Equal(probe.Steps, []byte("null")) {
		return true
	}

	var kind string
	if json.Unmarshal(probe.Type, &kind) != nil {
		return false
	}

	kind = strings.ToLower(kind)

	return kind == pingdomTMSScript || kind == pingdomTMSRecording
}

// fetchTMS lists the transaction checks (paginated), then reads each one. An
// account without the transaction product answers 404 and yields nothing;
// other failures besides a rejected token are reported as warnings so the
// classic checks still import.
func (c *PingdomConverter) fetchTMS(ctx context.Context, token string, warn *warnings) ([]json.RawMessage, error) {
	var ids []int64

	for page := range pingdomTMSMaxPages {
		path := "/api/3.1/tms/check?limit=" + strconv.Itoa(c.tmsPageSize) +
			"&offset=" + strconv.Itoa(page*c.tmsPageSize)

		body, err := c.get(ctx, token, path)
		if err != nil {
			return nil, c.tmsFailure(err, warn)
		}

		var list struct {
			Checks []struct {
				ID int64 `json:"id"`
			} `json:"checks"`
		}

		if json.Unmarshal(body, &list) != nil {
			warn.addDocf("the Pingdom transaction check list was unreadable; transaction checks were skipped")

			break
		}

		for _, item := range list.Checks {
			ids = append(ids, item.ID)
		}

		if len(list.Checks) < c.tmsPageSize {
			break
		}
	}

	results := make([]json.RawMessage, 0, len(ids))

	for _, id := range ids {
		body, err := c.get(ctx, token, "/api/3.1/tms/check/"+strconv.FormatInt(id, 10))
		if err != nil {
			return nil, err
		}

		results = append(results, unwrapPingdomItems([]json.RawMessage{body})[0])
	}

	return results, nil
}

// tmsFailure decides whether a transaction-list failure is fatal.
func (c *PingdomConverter) tmsFailure(err error, warn *warnings) error {
	switch {
	case errors.Is(err, ErrPingdomUnauthorized), errors.Is(err, ErrPingdomRateLimited),
		errors.Is(err, ErrPingdomUnreachable):
		return err
	case errors.Is(err, errPingdomNotFound):
		return nil
	default:
		warn.addDocf("the Pingdom transaction checks could not be listed (%s); they were skipped", err.Error())

		return nil
	}
}

// pingdomTMSTagNames reads tags, which are strings (or objects with a name
// when extended_tags is on).
func pingdomTMSTagNames(raw []json.RawMessage) []pingdomTag {
	tags := make([]pingdomTag, 0, len(raw))

	for _, item := range raw {
		var name string
		if json.Unmarshal(item, &name) == nil {
			tags = append(tags, pingdomTag{Name: name})

			continue
		}

		var tag pingdomTag
		if json.Unmarshal(item, &tag) == nil {
			tags = append(tags, tag)
		}
	}

	return tags
}

// convertPingdomTMS maps one transaction check to a JS browser check.
func convertPingdomTMS(raw json.RawMessage, slugs *slugSet, warn *warnings) (checks.ExportCheck, bool) {
	var source pingdomTMSCheck
	if err := json.Unmarshal(raw, &source); err != nil {
		warn.addf("transaction check", "", "check skipped: it could not be decoded")

		return checks.ExportCheck{}, false
	}

	name := source.Name
	if name == "" {
		name = "pingdom-transaction-" + strconv.FormatInt(source.ID, 10)
	}

	if len(source.Steps) == 0 {
		warn.addf(name, "steps", "check skipped: no steps found — paste the GET /tms/check/{id} response, "+
			"not the list, or use an API token")

		return checks.ExportCheck{}, false
	}

	gen := newPingdomScript(name, warn)
	gen.generate(&source)

	if len(source.Region) > 0 {
		warn.addf(name, "region", "region %q has no SolidPing counterpart — pick regions on the check", source.Region)
	}

	enabled := source.Active == nil || *source.Active
	if gen.untranslatable {
		enabled = false
	}

	config := map[string]any{"script": gen.String()}
	if len(gen.secrets) > 0 {
		secrets := map[string]any{}
		for key, value := range gen.secrets {
			secrets[key] = value
		}

		config["secrets"] = secrets
	}

	check := checks.ExportCheck{
		Name:    name,
		Slug:    slugs.unique(name),
		Type:    string(checkerdef.CheckTypeJS),
		Enabled: enabled,
		Period:  secondsToPeriod(source.Interval * 60),
		Config:  config,
		Labels:  pingdomLabels(pingdomTMSTagNames(source.Tags)),
	}

	if source.SendNotificationWhenDown > 0 && source.Interval > 0 {
		check.ConfirmationPeriodSeconds = intPtr(source.SendNotificationWhenDown * source.Interval * 60)
	}

	return check, true
}

// jsq renders s as a JS string literal. JSON is a subset of JS and Go escapes
// U+2028/U+2029, so a hostile value cannot break out of the literal.
func jsq(text string) string {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(text); err != nil {
		return `""`
	}

	return strings.TrimRight(buf.String(), "\n")
}

// pingdomScript accumulates the generated script.
type pingdomScript struct {
	name           string
	warn           *warnings
	body           []string
	helpers        map[string]bool
	secrets        map[string]string
	untranslatable bool
	// untranslatableCount counts the TODO lines emitted, so a step that only
	// produced a TODO comment is not wrapped in step().
	untranslatableCount int
	kind                string
}

func newPingdomScript(name string, warn *warnings) *pingdomScript {
	return &pingdomScript{name: name, warn: warn, helpers: map[string]bool{}, secrets: map[string]string{}}
}

// JS helper sources, emitted only when a step uses them.
const (
	helperMust = `function must(r, what) { if (!r.ok) { throw new Error(what + ": " + r.error); } }`

	helperExists = `function exists(sel) {
  var r = page.text(sel);
  return r.ok || /matches \d+ elements/.test(r.error || "");
}`

	helperWaitText = `function waitText(what, sel, value, isRegex, want, seconds) {
  var tries = Math.max(1, Math.ceil(seconds * 2));
  var has = false;
  for (var i = 0; i < tries; i++) {
    var t = page.text(sel);
    has = t.ok && (isRegex ? new RegExp(value).test(t.text) : t.text.indexOf(value) >= 0);
    if (has === want) { return; }
    if (i < tries - 1) { sleep(500); }
  }
  throw new Error(what + ": " + (want ? "text not found" : "text still present") + " in " + sel);
}`

	helperURLIs = `function urlIs(what, expected) {
  var got = page.url();
  if (got.replace(/\/+$/, "") !== expected.replace(/\/+$/, "")) {
    throw new Error(what + ": url is " + got + ", expected " + expected);
  }
}`

	helperPicked = `function pickedOption(opt, want) {
  if (!opt) { return false; }
  var n = function(s) { return String(s).replace(/\s+/g, " ").trim().toLowerCase(); };
  return n(opt[0]) === n(want) || opt[1] === want;
}`
)

// helperInPage runs a function body inside the page with the element
// resolved from a Pingdom selector (CSS, then name, then label text).
func helperInPage() string {
	return `var FIND = ` + findJS() + `;
function inPage(body, q, v) {
  var call = "})(" + FIND + "," + JSON.stringify(q) + "," + JSON.stringify(v) + ")";
  var r = page.evaluate("(function(find, q, v) {" + body + call);
  if (!r.ok) { throw new Error("page evaluation failed: " + r.error); }
  return r.value;
}`
}

// findJS is the page-side resolver, as a JS string literal.
func findJS() string {
	return jsq(`function(q) {
  var n = function(s) { return (s || "").replace(/\s+/g, " ").trim().toLowerCase(); };
  try { var e = document.querySelector(q); if (e) { return e; } } catch (x) {}
  var byName = document.querySelector('[name="' + q.replace(/"/g, '\\"') + '"]');
  if (byName) { return byName; }
  var labels = document.querySelectorAll("label");
  for (var i = 0; i < labels.length; i++) {
    if (n(labels[i].textContent) === n(q) && labels[i].control) { return labels[i].control; }
  }
  return null;
}`)
}

// Helper names.
const (
	helperNameMust     = "must"
	helperNameExists   = "exists"
	helperNameWaitText = "waitText"
	helperNameURLIs    = "urlIs"
	helperNameInPage   = "inPage"
	helperNamePicked   = "pickedOption"
)

// helperOrder fixes the emission order.
func helperOrder() []string {
	return []string{
		helperNameMust, helperNameExists, helperNameWaitText, helperNameURLIs, helperNameInPage, helperNamePicked,
	}
}

// helperSource returns the source of one named helper.
func helperSource(name string) string {
	switch name {
	case helperNameMust:
		return helperMust
	case helperNameExists:
		return helperExists
	case helperNameWaitText:
		return helperWaitText
	case helperNameURLIs:
		return helperURLIs
	case helperNameInPage:
		return helperInPage()
	default:
		return helperPicked
	}
}

// String renders the finished script.
func (g *pingdomScript) String() string {
	var out strings.Builder

	out.WriteString("// Generated from Pingdom transaction check " + jsq(g.name) + " (" + g.kind + ").\n")
	out.WriteString("var page = browser.open();\n")

	for _, key := range helperOrder() {
		if g.helpers[key] {
			out.WriteString(helperSource(key) + "\n")
		}
	}

	for _, line := range g.body {
		out.WriteString(line + "\n")
	}

	out.WriteString(`return { status: "up", output: { url: page.url() } };` + "\n")

	return out.String()
}

func (g *pingdomScript) emitf(format string, args ...any) {
	g.body = append(g.body, fmt.Sprintf(format, args...))
}

func (g *pingdomScript) use(helpers ...string) {
	for _, helper := range helpers {
		g.helpers[helper] = true
	}
}

// todo records an untranslatable step: a warning, a TODO line, a paused check.
func (g *pingdomScript) todo(index int, step pingdomTMSStep, reason string) {
	g.untranslatable = true
	g.untranslatableCount++
	g.warn.addf(g.name, "steps", "step %d (%s) %s — the check was created paused", index+1, jsq(step.Fn), reason)
	g.emitf("// TODO step %d: %s %s", index+1, jsq(step.Fn), reason)
}

// generate walks the steps and emits the script body.
func (g *pingdomScript) generate(check *pingdomTMSCheck) {
	g.kind = check.Type
	if g.kind == "" {
		g.kind = pingdomTMSScript
	}

	g.collectBasicAuth(check)

	authDone := false

	for index, step := range check.Steps {
		if step.Fn == "go_to" || step.Fn == "wpm_go_to" {
			if !authDone {
				g.emitBasicAuth()

				authDone = true
			}
		}

		before, todoBefore := len(g.body), g.untranslatableCount
		g.step(index, step)
		g.wrapStep(before, todoBefore, step)
	}

	if !authDone && len(g.secrets) > 0 {
		g.warn.addf(g.name, "steps", "basic auth credentials were stored but the check has no go_to step to arm them")
	}
}

// collectBasicAuth pulls credentials out of basic_auth steps and recording
// metadata into the encrypted secrets, never the script.
func (g *pingdomScript) collectBasicAuth(check *pingdomTMSCheck) {
	for _, step := range check.Steps {
		if step.Fn == "basic_auth" && g.secrets[pingdomSecretUser] == "" {
			g.secrets[pingdomSecretUser] = step.arg("username")
			g.secrets[pingdomSecretPassword] = step.arg("password")
		}
	}

	auths := check.Metadata.Authentications.HTTPAuthentications
	if len(auths) == 0 {
		return
	}

	if g.secrets[pingdomSecretUser] == "" {
		g.secrets[pingdomSecretUser] = auths[0].Credentials.UserName
		g.secrets[pingdomSecretPassword] = auths[0].Credentials.Password
	}

	if len(auths) > 1 {
		g.warn.addf(g.name, "metadata", "%d HTTP authentications found; only the first was imported", len(auths))
	}
}

// emitBasicAuth arms the credentials before the first navigation.
func (g *pingdomScript) emitBasicAuth() {
	if g.secrets[pingdomSecretUser] == "" && g.secrets[pingdomSecretPassword] == "" {
		delete(g.secrets, pingdomSecretUser)
		delete(g.secrets, pingdomSecretPassword)

		return
	}

	g.use(helperNameMust)
	g.emitf("must(page.basicAuth(secrets.%s, secrets.%s), %s);",
		pingdomSecretUser, pingdomSecretPassword, jsq("basic_auth"))
}

// sel wraps a Pingdom selector as an auto= selector.
func sel(raw string) string {
	return jsq("auto=" + raw)
}

// label is the failure message prefix of a step.
func stepLabel(index int, step pingdomTMSStep, detail string) string {
	return jsq(fmt.Sprintf("step %d: %s %s", index+1, step.Fn, detail))
}

// normalizeURL adds https:// to a scheme-less URL; ok is false for a non-http scheme.
func normalizeURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}

	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}

	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", false
	}

	return raw, true
}

// seconds parses a seconds argument and clamps it into [0, max].
func (g *pingdomScript) seconds(index int, step pingdomTMSStep, raw string, fallback float64) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(value) || value < 0 {
		return fallback
	}

	if value > pingdomMaxSleepSeconds {
		g.warn.addf(g.name, "steps", "step %d (%s) waits %gs, clamped to %ds",
			index+1, step.Fn, value, pingdomMaxSleepSeconds)

		return pingdomMaxSleepSeconds
	}

	return value
}

// step translates one step.
//
//nolint:cyclop // one case per Pingdom function
func (g *pingdomScript) step(index int, step pingdomTMSStep) {
	switch step.Fn {
	case "go_to", "wpm_go_to":
		target, ok := normalizeURL(step.arg("url"))
		if !ok {
			g.todo(index, step, "has an unusable url")

			return
		}

		g.use(helperNameMust)
		g.emitf("must(page.goto(%s), %s);", jsq(target), stepLabel(index, step, target))
	case "click", "wpm_click":
		g.action(index, step, "page.click(%s)", step.arg("element"))
	case "fill", "wpm_fill":
		g.use(helperNameMust)
		g.emitf("must(page.fill(%s, %s), %s);", sel(step.arg("input")), jsq(step.arg("value")),
			stepLabel(index, step, step.arg("input")))
	case "check":
		g.action(index, step, "page.check(%s)", step.arg("checkbox"))
	case "uncheck":
		g.action(index, step, "page.uncheck(%s)", step.arg("checkbox"))
	case "select", "wpm_select_dropdown":
		g.use(helperNameMust)
		g.emitf("must(page.select(%s, %s), %s);", sel(step.arg("select")), jsq(step.arg("option")),
			stepLabel(index, step, step.arg("select")))
	case "select_radio":
		g.warn.addf(g.name, "steps", "step %d (select_radio) resolves the radio %q with auto=; "+
			"if several radios share that name the step fails as ambiguous — use label= instead",
			index+1, step.arg("radio"))
		g.action(index, step, "page.check(%s)", step.arg("radio"))
	case "submit":
		g.action(index, step, "page.submit(%s)", step.arg("form"))
	case "sleep", "wpm_wait":
		secs := g.seconds(index, step, step.arg("seconds"), 1)
		g.emitf("sleep(%d);", int(math.Round(secs*1000)))
	case "basic_auth":
		// Hoisted before the first go_to by generate().
	case "wait_for_element":
		g.action(index, step, "page.waitFor(%s)", step.arg("element"))
	case "wait_for_contains":
		g.waitText(index, step, step.arg("element"), step.arg("value"), false, true,
			g.seconds(index, step, "", pingdomDefaultWaitSeconds))
	case "wpm_contains_timeout", "wpm_not_contains_timeout":
		g.waitText(index, step, step.arg("element"), step.arg("value"), step.arg("useRegularExpression") == "true",
			step.Fn == "wpm_contains_timeout", g.seconds(index, step, step.arg("waitTime"), 0))
	default:
		g.validation(index, step)
	}
}

// action emits must(<call>, label) for a single-selector call.
func (g *pingdomScript) action(index int, step pingdomTMSStep, callFormat, element string) {
	g.use(helperNameMust)
	g.emitf("must("+callFormat+", %s);", sel(element), stepLabel(index, step, element))
}

// waitText emits a poll over an element's text.
func (g *pingdomScript) waitText(
	index int, step pingdomTMSStep, element, value string, isRegex, want bool, seconds float64,
) {
	g.use(helperNameWaitText)
	g.emitf("waitText(%s, %s, %s, %t, %t, %s);", stepLabel(index, step, element), sel(element),
		jsq(value), isRegex, want, strconv.FormatFloat(seconds, 'f', -1, 64))
}

// validation translates the verification steps, and flags unknown functions.
func (g *pingdomScript) validation(index int, step pingdomTMSStep) {
	switch step.Fn {
	case "url":
		expected, ok := normalizeURL(step.arg("url"))
		if !ok {
			g.todo(index, step, "has an unusable url")

			return
		}

		g.use(helperNameURLIs)
		g.emitf("urlIs(%s, %s);", stepLabel(index, step, expected), jsq(expected))
	case helperNameExists, "not_exists":
		g.use(helperNameExists)

		negate, reason := "", "still present"
		if step.Fn == helperNameExists {
			negate, reason = "!", "not found"
		}

		g.emitf("if (%sexists(%s)) { throw new Error(%s); }", negate, sel(step.arg("element")),
			jsq(fmt.Sprintf("step %d: %s: element %s %s", index+1, step.Fn, step.arg("element"), reason)))
	case "contains", "not_contains":
		g.waitText(index, step, step.arg("element"), step.arg("value"), false, step.Fn == "contains", 0)
	case "field_contains", "field_not_contains":
		g.use(helperNameInPage)
		g.emitf("if ((String(inPage(%s, %s, null)).indexOf(%s) >= 0) !== %t) { throw new Error(%s); }",
			jsq(`var e = find(q); return e ? String(e.value) : "";`), jsq(step.arg("input")), jsq(step.arg("value")),
			step.Fn == "field_contains",
			stepLabel(index, step, step.arg("input")+" value check failed"))
	case "is_checked", "is_not_checked":
		g.use(helperNameInPage)
		g.emitf("if (inPage(%s, %s, null) !== %t) { throw new Error(%s); }",
			jsq(`var e = find(q); return e ? !!e.checked : null;`), jsq(step.arg("checkbox")),
			step.Fn == "is_checked", stepLabel(index, step, step.arg("checkbox")+" checked state mismatch"))
	case "radio_selected":
		g.use(helperNameInPage)
		g.emitf("if (inPage(%s, %s, null) !== true) { throw new Error(%s); }",
			jsq(`return !!document.querySelector('input[type=radio][name="' + q.replace(/"/g, '\\"') + '"]:checked');`),
			jsq(step.arg("radio")), stepLabel(index, step, step.arg("radio")+" is not selected"))
	case "dropdown_selected", "dropdown_not_selected":
		g.use(helperNameInPage, helperNamePicked)
		g.emitf("if (pickedOption(inPage(%s, %s, null), %s) !== %t) { throw new Error(%s); }",
			jsq(`var e = find(q); if (!e || !e.options || e.selectedIndex < 0) { return null; } `+
				`var o = e.options[e.selectedIndex]; return [o.text, o.value];`),
			jsq(step.arg("select")), jsq(step.arg("option")), step.Fn == "dropdown_selected",
			stepLabel(index, step, step.arg("select")+" option check failed"))
	case "wpm_click_xy", "wpm_move_mouse_to_element", "wpm_close_tab":
		g.todo(index, step, "has no SolidPing equivalent (coordinate, hover and tab steps are not supported)")
	default:
		g.todo(index, step, "is not a known Pingdom function")
	}
}

// wrapStep wraps what a step emitted in step("<label>", …) so a failing run
// reports which step failed (spec 2026-11-17-04). A step that emitted nothing
// (basic_auth, hoisted) or only a TODO line stays as is.
func (g *pingdomScript) wrapStep(before, todoBefore int, step pingdomTMSStep) {
	if len(g.body) == before || g.untranslatableCount != todoBefore {
		return
	}

	lines := slices.Clone(g.body[before:])
	g.body = g.body[:before]

	g.emitf("step(%s, function () {", jsq(stepHumanLabel(step)))

	for _, line := range lines {
		g.emitf("  %s", line)
	}

	g.emitf("});")
}

// stepHumanLabelArgs are the arguments that name a step's target, in order.
//
//nolint:gochecknoglobals // fixed list
var stepHumanLabelArgs = []string{"element", "input", "select", "checkbox", "radio", "form", "url"}

// stepHumanLabel is the name of a step in the result: the function spelled
// out and its target, e.g. `Click "Se connecter"` or `Wait for element "#ok"`.
func stepHumanLabel(step pingdomTMSStep) string {
	words := strings.TrimPrefix(strings.ReplaceAll(step.Fn, "_", " "), "wpm ")
	label := strings.ToUpper(words[:1]) + words[1:]

	for _, key := range stepHumanLabelArgs {
		if target := step.arg(key); target != "" {
			return label + ` "` + target + `"`
		}
	}

	return label
}
