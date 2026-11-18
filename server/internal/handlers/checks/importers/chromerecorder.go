package importers

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// ErrRecorderInvalid is returned when the payload is not a usable Chrome
// DevTools Recorder export.
var ErrRecorderInvalid = errors.New("not a Chrome DevTools Recorder export")

// Recorder step types (the @puppeteer/replay user-flow schema).
const (
	recNavigate        = "navigate"
	recClick           = "click"
	recDoubleClick     = "doubleClick"
	recChange          = "change"
	recKeyDown         = "keyDown"
	recKeyUp           = "keyUp"
	recWaitForElement  = "waitForElement"
	recWaitExpression  = "waitForExpression"
	recHover           = "hover"
	recScroll          = "scroll"
	recSetViewport     = "setViewport"
	recEmulateNetwork  = "emulateNetworkConditions"
	recClose           = "close"
	recCustomStep      = "customStep"
	recSecretPrefix    = "RECORDER_SECRET_"
	recDefaultTimeout  = 5000
	recMaxTimeoutMs    = 30000
	recCaptureWidth    = 1280
	recCaptureHeight   = 800
	recViewportRatio   = 2
	recLabelMaxLen     = 80
	recPrimaryButton   = "primary"
	recMainTarget      = "main"
	recOperatorEqual   = "=="
	recOperatorAtMost  = "<="
	recOperatorAtLeast = ">="
)

// recorderFlow is the top-level export.
type recorderFlow struct {
	Title   string            `json:"title"`
	Timeout int               `json:"timeout"`
	Steps   []json.RawMessage `json:"steps"`
}

// recorderStep is the subset of a Recorder step we map.
type recorderStep struct {
	Type           string            `json:"type"`
	URL            string            `json:"url"`
	Selectors      []json.RawMessage `json:"selectors"`
	Frame          []int             `json:"frame"`
	Target         string            `json:"target"`
	Timeout        *int              `json:"timeout"`
	Value          string            `json:"value"`
	Key            string            `json:"key"`
	Button         string            `json:"button"`
	Operator       string            `json:"operator"`
	Count          *int              `json:"count"`
	Visible        *bool             `json:"visible"`
	Attributes     map[string]string `json:"attributes"`
	Properties     map[string]any    `json:"properties"`
	Expression     string            `json:"expression"`
	Width          int               `json:"width"`
	Height         int               `json:"height"`
	AssertedEvents []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"assertedEvents"`
}

// ChromeRecorderConverter converts a Chrome DevTools Recorder JSON export into
// one JS browser check. It is pure: no I/O.
type ChromeRecorderConverter struct{}

// Source implements Converter.
func (*ChromeRecorderConverter) Source() string { return SourceChromeRecorder }

// Convert implements Converter.
func (*ChromeRecorderConverter) Convert(input []byte) (*ConversionResult, error) {
	var flow recorderFlow
	if err := json.Unmarshal(input, &flow); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON: %w", ErrRecorderInvalid, err)
	}

	if len(flow.Steps) == 0 {
		return nil, fmt.Errorf("%w: it has no steps", ErrRecorderInvalid)
	}

	steps := make([]*recorderStep, 0, len(flow.Steps))

	for index, raw := range flow.Steps {
		step := &recorderStep{}
		if err := json.Unmarshal(raw, step); err != nil {
			// Keep the type so the step becomes a TODO instead of failing the import.
			var head struct {
				Type string `json:"type"`
			}

			_ = json.Unmarshal(raw, &head)
			step = &recorderStep{Type: head.Type}

			if head.Type == "" {
				return nil, fmt.Errorf("%w: step %d has no type", ErrRecorderInvalid, index+1)
			}
		}

		steps = append(steps, step)
	}

	if err := requireNavigateFirst(steps); err != nil {
		return nil, err
	}

	warn := &warnings{}

	name := strings.TrimSpace(flow.Title)
	if name == "" {
		name = "Chrome recording"
	}

	gen := &recorderScript{
		name: name, warn: warn, flowTimeout: flow.Timeout,
		helpers: map[string]bool{}, secrets: map[string]string{}, lastSelectors: jsq("body"),
	}
	gen.generate(steps)

	config := map[string]any{"script": gen.String()}
	if len(gen.secrets) > 0 {
		secrets := map[string]any{}
		for key, value := range gen.secrets {
			secrets[key] = value
		}

		config["secrets"] = secrets
	}

	doc := newDocument(SourceChromeRecorder)
	doc.Checks = append(doc.Checks, checks.ExportCheck{
		Name:    name,
		Slug:    newSlugSet().unique(name),
		Type:    string(checkerdef.CheckTypeJS),
		Enabled: !gen.untranslatable,
		Config:  config,
	})

	normalizeChecks(doc, warn)

	return &ConversionResult{Document: doc, Warnings: warn.result()}, nil
}

// requireNavigateFirst checks that the flow opens a page before anything else.
func requireNavigateFirst(steps []*recorderStep) error {
	for _, step := range steps {
		switch step.Type {
		case recSetViewport, recEmulateNetwork:
			continue
		case recNavigate:
			return nil
		default:
			return fmt.Errorf("%w: the first step must be a navigate step, found %q "+
				"(record from a new page so the flow starts with its URL)", ErrRecorderInvalid, step.Type)
		}
	}

	return fmt.Errorf("%w: it has no navigate step", ErrRecorderInvalid)
}

// recorderScript accumulates the generated script.
type recorderScript struct {
	name        string
	warn        *warnings
	flowTimeout int
	body        []string
	helpers     map[string]bool
	secrets     map[string]string
	// typedLiterals counts the change values left in the script.
	typedLiterals  int
	untranslatable bool
	todoCount      int
	lastSelectors  string
}

const (
	recHelperMust = "must"
	recHelperPoll = "poll"
	recHelperURL  = "url"
)

const (
	recMustSrc = `function must(r, what) { if (!r.ok) { throw new Error(what + ": " + r.error); } }`
	recPollSrc = `function poll(what, expr, ms) {
  var tries = Math.max(1, Math.ceil(ms / 500));
  for (var i = 0; i < tries; i++) {
    var r = page.evaluate(expr);
    if (r.ok && r.value) { return; }
    if (i < tries - 1) { sleep(500); }
  }
  throw new Error(what + ": condition not met");
}`
	recURLSrc = `function waitUrl(want) {
  var trim = function (u) { return String(u).replace(/\/$/, ""); };
  for (var i = 0; i < 10; i++) {
    if (trim(page.url()) === trim(want)) { return; }
    sleep(500);
  }
  throw new Error("expected navigation to " + want + " but the page is at " + page.url());
}`
)

// String renders the finished script.
func (g *recorderScript) String() string {
	var out strings.Builder

	out.WriteString("// Generated from Chrome DevTools Recorder flow " + jsq(g.name) + ".\n")
	out.WriteString("const page = browser.open();\n")

	for _, key := range []string{recHelperMust, recHelperPoll, recHelperURL} {
		if !g.helpers[key] {
			continue
		}

		switch key {
		case recHelperMust:
			out.WriteString(recMustSrc + "\n")
		case recHelperPoll:
			out.WriteString(recPollSrc + "\n")
		case recHelperURL:
			out.WriteString(recURLSrc + "\n")
		}
	}

	for _, line := range g.body {
		out.WriteString(line + "\n")
	}

	out.WriteString(`return { status: "up", output: { url: page.url() } };` + "\n")

	return out.String()
}

func (g *recorderScript) emitf(format string, args ...any) {
	g.body = append(g.body, fmt.Sprintf(format, args...))
}

// todo records an unsupported step: a warning, a TODO line, a paused check.
func (g *recorderScript) todo(index int, step *recorderStep, reason string) {
	g.untranslatable = true
	g.todoCount++
	g.warn.addf(g.name, "steps", "step %d (%s) %s — the check was created paused", index+1, jsq(step.Type), reason)
	g.emitf("// TODO step %d: %s %s", index+1, jsq(step.Type), reason)
}

// warnStep records a warning that does not pause the check.
func (g *recorderScript) warnStepf(index int, step *recorderStep, format string, args ...any) {
	g.warn.addf(g.name, "steps", "step %d (%s) %s", index+1, jsq(step.Type), fmt.Sprintf(format, args...))
}

// generate walks the steps and emits the body.
func (g *recorderScript) generate(steps []*recorderStep) {
	for index := 0; index < len(steps); index++ {
		step := steps[index]
		before, todoBefore := len(g.body), g.todoCount

		label := recorderLabel(step)

		// A keyDown directly followed by the matching keyUp is one key press.
		if step.Type == recKeyDown && index+1 < len(steps) &&
			steps[index+1].Type == recKeyUp && steps[index+1].Key == step.Key {
			g.press(index, step)

			index++
			g.afterStep(steps[index], todoBefore)
		} else {
			g.step(index, step)
		}

		g.afterStep(step, todoBefore)
		g.wrap(before, todoBefore, label)
	}

	if len(g.secrets) > 0 {
		keys := make([]string, 0, len(g.secrets))
		for key := range g.secrets {
			keys = append(keys, key)
		}

		sort.Strings(keys)
		g.warn.addf(g.name, "secrets",
			"%d typed value(s) were moved to the check's encrypted secrets (%s) and are not in the script",
			len(keys), strings.Join(keys, ", "))
	}

	if g.typedLiterals > 0 {
		g.warn.addf(g.name, "steps", typedValuesLeftFormat, g.typedLiterals)
	}
}

// step translates one non-keyboard step.
func (g *recorderScript) step(index int, step *recorderStep) {
	if len(step.Frame) > 0 {
		g.todo(index, step, "runs inside an iframe, which a check cannot drive")

		return
	}

	if step.Target != "" && step.Target != recMainTarget {
		g.todo(index, step, "targets another page (popup or new tab); a check drives a single page")

		return
	}

	switch step.Type {
	case recNavigate:
		g.use(recHelperMust)
		g.emitf("must(page.goto(%s), %s);", jsq(step.URL), jsq(stepWhat(index, step.Type+" "+step.URL)))
	case recClick, recDoubleClick:
		g.click(index, step)
	case recChange:
		g.change(index, step)
	case recKeyDown, recKeyUp:
		g.warnStepf(index, step, "is a lone key event (modifier or unmatched) and was skipped")
		g.emitf("// step %d: lone %s %s skipped", index+1, step.Type, jsq(step.Key))
	case recWaitForElement:
		g.waitForElement(index, step)
	case recWaitExpression:
		g.use(recHelperPoll)
		g.emitf("poll(%s, %s, %d);", jsq(stepWhat(index, "waitForExpression")), jsq(step.Expression), g.timeoutMs(step))
	case recHover, recScroll, recEmulateNetwork, recClose, recSetViewport:
		g.passive(index, step)
	case recCustomStep:
		g.todo(index, step, "is a custom step, which has no SolidPing equivalent")
	default:
		g.todo(index, step, "is not a known Recorder step type")
	}
}

// passive handles the steps with no effect on a check.
func (g *recorderScript) passive(index int, step *recorderStep) {
	if step.Type != recSetViewport {
		g.emitf("// step %d: %s has no effect on a check and was skipped", index+1, step.Type)

		return
	}

	g.emitf("// step %d: setViewport %dx%d ignored (the check uses a fixed viewport)", index+1, step.Width, step.Height)

	if differsFromCapture(step.Width, recCaptureWidth) || differsFromCapture(step.Height, recCaptureHeight) {
		g.warnStepf(index, step, "sets a %dx%d viewport but checks run at %dx%d; the page may lay out differently",
			step.Width, step.Height, recCaptureWidth, recCaptureHeight)
	}
}

// differsFromCapture reports a size far from the capture viewport.
func differsFromCapture(got, want int) bool {
	return got > 0 && (got*recViewportRatio < want || got > want*recViewportRatio)
}

func (g *recorderScript) use(helper string) { g.helpers[helper] = true }

func stepWhat(index int, what string) string {
	return "step " + strconv.Itoa(index+1) + ": " + what
}

// selectors flattens a step's selectors into plain selector strings. An entry
// is itself a path whose last element is the target and earlier ones are
// frames or shadow hosts: only single-element entries are kept.
func (g *recorderScript) selectors(index int, step *recorderStep) []string {
	var out []string

	skipped := 0

	for _, raw := range step.Selectors {
		var path []string
		if err := json.Unmarshal(raw, &path); err != nil {
			var single string
			if err := json.Unmarshal(raw, &single); err != nil {
				skipped++

				continue
			}

			path = []string{single}
		}

		if len(path) == 1 {
			out = append(out, path[0])
		} else if len(path) > 1 {
			skipped++
		}
	}

	if skipped > 0 {
		g.warnStepf(index, step, "has %d selector(s) crossing a shadow root or frame that were skipped", skipped)
	}

	return out
}

// selectorLiteral renders selectors as a JS array literal (the engine tries
// them in order).
func selectorLiteral(list []string) string {
	parts := make([]string, len(list))
	for i, item := range list {
		parts[i] = jsq(item)
	}

	return "[" + strings.Join(parts, ", ") + "]"
}

func (g *recorderScript) click(index int, step *recorderStep) {
	list := g.selectors(index, step)
	if len(list) == 0 {
		g.todo(index, step, "has no usable selector")

		return
	}

	if step.Button != "" && step.Button != recPrimaryButton {
		g.warnStepf(index, step, "uses the %q mouse button, which is not supported; the click was skipped", step.Button)
		g.emitf("// step %d: %s click skipped", index+1, step.Button)

		return
	}

	g.use(recHelperMust)

	literal := selectorLiteral(list)
	g.lastSelectors = literal

	times := 1
	if step.Type == recDoubleClick {
		times = 2

		g.emitf("// double click: two clicks in a row")
	}

	for range times {
		g.emitf("must(page.click(%s), %s);", literal, jsq(stepWhat(index, "click "+recorderTarget(list))))
	}
}

func (g *recorderScript) change(index int, step *recorderStep) {
	list := g.selectors(index, step)
	if len(list) == 0 {
		g.todo(index, step, "has no usable selector")

		return
	}

	g.use(recHelperMust)

	literal := selectorLiteral(list)
	g.lastSelectors = literal

	value := jsq(step.Value)

	if isTypedSecret(step.Value, list...) {
		key := recSecretPrefix + strconv.Itoa(len(g.secrets)+1)
		g.secrets[key] = step.Value
		value = "secrets." + key
	} else {
		g.typedLiterals++
	}

	g.emitf("must(page.fill(%s, %s), %s);", literal, value, jsq(stepWhat(index, "fill "+recorderTarget(list))))
}

func (g *recorderScript) press(index int, step *recorderStep) {
	g.use(recHelperMust)
	g.emitf("must(page.press(%s, %s), %s);", g.lastSelectors, jsq(step.Key), jsq(stepWhat(index, "press "+step.Key)))
}

// timeoutMs resolves a step's timeout, clamped to the script budget.
func (g *recorderScript) timeoutMs(step *recorderStep) int {
	timeout := recDefaultTimeout

	switch {
	case step.Timeout != nil && *step.Timeout > 0:
		timeout = *step.Timeout
	case g.flowTimeout > 0:
		timeout = g.flowTimeout
	}

	return min(timeout, recMaxTimeoutMs)
}

func (g *recorderScript) waitForElement(index int, step *recorderStep) {
	list := g.selectors(index, step)
	if len(list) == 0 {
		g.todo(index, step, "has no usable selector")

		return
	}

	g.use(recHelperMust)

	literal := selectorLiteral(list)
	timeout := g.timeoutMs(step)
	what := jsq(stepWhat(index, "waitForElement "+recorderTarget(list)))
	visible := step.Visible == nil || *step.Visible

	if visible {
		g.emitf("must(page.waitFor(%s, { timeout: %d }), %s);", literal, timeout, what)
	}

	expr, ok := g.assertionExpr(list, step, visible)
	if !ok {
		return
	}

	if expr == "" {
		return
	}

	g.use(recHelperPoll)
	g.emitf("poll(%s, %s, %d);", what, jsq(expr), timeout)
}

// assertionExpr builds the in-page check for count, visibility, attributes and
// properties. It returns "" when waitFor alone is enough.
func (g *recorderScript) assertionExpr(list []string, step *recorderStep, visible bool) (string, bool) {
	operator := step.Operator
	if operator == "" {
		operator = recOperatorAtLeast
	}

	wantCount := step.Count != nil && (operator != recOperatorAtLeast || *step.Count > 1)
	needed := wantCount || !visible || len(step.Attributes) > 0 || len(step.Properties) > 0

	if !needed {
		return "", true
	}

	query := inPageQuery(list)
	if query == "" {
		g.warn.addf(g.name, "steps",
			"a waitForElement assertion (count, visibility, attributes or properties) needs a CSS or XPath selector; "+
				"only the presence wait was kept")

		return "", false
	}

	const isShown = `function (e) { return !!(e.offsetWidth || e.offsetHeight || e.getClientRects().length); }`

	var expr strings.Builder

	expr.WriteString("(function () { var els = " + query + "; var shown = " + isShown + "; ")

	if !visible {
		expr.WriteString("return els.every(function (e) { return !shown(e); }); })()")

		return expr.String(), true
	}

	expr.WriteString("els = els.filter(shown); ")

	if wantCount {
		switch operator {
		case recOperatorEqual, recOperatorAtMost, recOperatorAtLeast:
		default:
			operator = recOperatorAtLeast
		}

		jsOp := operator
		if operator == recOperatorEqual {
			jsOp = "==="
		}

		fmt.Fprintf(&expr, "if (!(els.length %s %d)) { return false; } ", jsOp, *step.Count)
	}

	expr.WriteString(attributeChecks(step))

	expr.WriteString("return true; })()")

	return expr.String(), true
}

// attributeChecks renders the attribute and property comparisons on the first match.
func attributeChecks(step *recorderStep) string {
	if len(step.Attributes) == 0 && len(step.Properties) == 0 {
		return ""
	}

	return "var el = els[0]; if (!el) { return false; } var attrs = " + jsonLiteral(step.Attributes) +
		"; var props = " + jsonLiteral(step.Properties) + "; " +
		"for (var a in attrs) { if (el.getAttribute(a) !== attrs[a]) { return false; } } " +
		"for (var p in props) { if (JSON.stringify(el[p]) !== JSON.stringify(props[p])) { return false; } } "
}

// jsonLiteral renders a value as a JS literal (JSON is a subset of JS).
func jsonLiteral(value any) string {
	// The values are decoded JSON, so they always marshal back.
	encoded, _ := json.Marshal(value) //nolint:errchkjson // decoded JSON always marshals

	return string(encoded)
}

// inPageQuery returns a JS expression listing the elements of the first
// selector that can be evaluated in the page (CSS or XPath), or "".
func inPageQuery(list []string) string {
	for _, selector := range list {
		switch {
		case strings.HasPrefix(selector, "xpath/"):
			return `(function () { var r = document.evaluate(` + jsq(strings.TrimPrefix(selector, "xpath/")) +
				`, document, null, 7, null), o = []; for (var i = 0; i < r.snapshotLength; i++) { ` +
				`o.push(r.snapshotItem(i)); } return o; })()`
		case strings.HasPrefix(selector, "aria/"), strings.HasPrefix(selector, "text/"):
			continue
		case strings.HasPrefix(selector, "pierce/"):
			return `Array.prototype.slice.call(document.querySelectorAll(` +
				jsq(strings.TrimPrefix(selector, "pierce/")) + `))`
		default:
			return `Array.prototype.slice.call(document.querySelectorAll(` + jsq(selector) + `))`
		}
	}

	return ""
}

// afterStep asserts the navigation a step is declared to trigger.
func (g *recorderScript) afterStep(step *recorderStep, todoBefore int) {
	if g.todoCount != todoBefore {
		return
	}

	for i := range step.AssertedEvents {
		event := &step.AssertedEvents[i]

		if event.Type != "navigation" || event.URL == "" {
			continue
		}

		g.use(recHelperURL)
		g.emitf("waitUrl(%s);", jsq(event.URL))
	}
}

// wrap wraps what a step emitted in step("<label>", …). Steps that only
// produced a comment stay as they are.
func (g *recorderScript) wrap(before, todoBefore int, label string) {
	if len(g.body) == before || g.todoCount != todoBefore {
		return
	}

	lines := slices.Clone(g.body[before:])

	hasCode := false

	for _, line := range lines {
		if !strings.HasPrefix(line, "//") {
			hasCode = true
		}
	}

	if !hasCode {
		return
	}

	g.body = g.body[:before]

	g.emitf("step(%s, function () {", jsq(label))

	for _, line := range lines {
		for _, sub := range strings.Split(line, "\n") {
			g.emitf("  %s", sub)
		}
	}

	g.emitf("});")
}

// recorderTarget names a step's target from its first selector.
func recorderTarget(list []string) string {
	target := list[0]
	target = strings.TrimPrefix(target, "aria/")

	if i := strings.Index(target, "[role="); i > 0 {
		target = target[:i]
	}

	if len(target) > recLabelMaxLen {
		target = target[:recLabelMaxLen]
	}

	return target
}

// recorderLabel is the step's name in the result.
func recorderLabel(step *recorderStep) string {
	var first string

	for _, raw := range step.Selectors {
		var path []string
		if json.Unmarshal(raw, &path) == nil && len(path) == 1 {
			first = recorderTarget(path[:1])

			break
		}
	}

	quote := func(text string) string { return ` "` + text + `"` }

	switch step.Type {
	case recNavigate:
		return "Go to" + quote(step.URL)
	case recClick:
		return "Click" + quote(first)
	case recDoubleClick:
		return "Double click" + quote(first)
	case recChange:
		return "Fill" + quote(first)
	case recKeyDown, recKeyUp:
		return "Press" + quote(step.Key)
	case recWaitForElement:
		return "Wait for element" + quote(first)
	case recWaitExpression:
		return "Wait for expression"
	default:
		return step.Type
	}
}
