package aichecks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkjs"
	"github.com/fclairamb/solidping/server/internal/egress"
)

// ErrURLRequired is a fetch or snapshot without a URL.
var ErrURLRequired = errors.New("url is required")

const (
	fetchBodyLimit    = 16 * 1024
	snapshotLimit     = 24 * 1024
	modelOutputLimit  = 24 * 1024
	scrubPlaceholder  = "[secret:%s]"
	toolScriptTimeout = 30 * time.Second
)

// RunResult is a script run as handed back to a caller (the model, the
// dashboard, an MCP agent). Secret values are already scrubbed from it.
type RunResult struct {
	Status     string         `json:"status"`
	DurationMs int64          `json:"durationMs"`
	Output     map[string]any `json:"output,omitempty"`
	Metrics    map[string]any `json:"metrics,omitempty"`
}

// Up reports a passing run.
func (r *RunResult) Up() bool {
	return r != nil && r.Status == checkerdef.StatusUp.String()
}

// Runner executes js scripts through the regular js checker, so every rule a
// js check obeys (egress, call budget, timeouts, browser slots) applies to the
// authoring tools too.
type Runner struct {
	// Guard returns the egress guard outbound connections go through, read
	// at call time (the server installs its guard after startup). Nil, or a
	// nil guard, allows everything (tests).
	Guard func() *egress.Guard
}

// Run executes a script. secrets carries the values the script may use; they
// are scrubbed from the returned result.
func (r *Runner) Run(
	ctx context.Context, script string, env, secrets map[string]string, timeout time.Duration,
) (*RunResult, error) {
	if timeout <= 0 || timeout > toolScriptTimeout {
		timeout = toolScriptTimeout
	}

	cfg := &checkjs.JSConfig{Script: script, Env: env, Secrets: secrets, Timeout: timeout}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if r.Guard != nil {
		if guard := r.Guard(); guard != nil {
			ctx = egress.WithGuard(ctx, guard)
		}
	}

	result, err := (&checkjs.JSChecker{}).Execute(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("running the script: %w", err)
	}

	out := &RunResult{
		Status:     result.Status.String(),
		DurationMs: result.Duration.Milliseconds(),
		Output:     result.Output,
		Metrics:    result.Metrics,
	}

	scrubResult(out, secrets)

	return out, nil
}

// fetchScript fetches env.URL and reports status, headers and a truncated body.
const fetchScript = `var r = http.get(env.URL);
if (r.error) { return { status: "down", output: { error: r.error } }; }
var body = r.body || "";
return { status: "up", output: {
  statusCode: r.statusCode, url: r.url, headers: r.headers,
  bodyLength: body.length, body: body.substring(0, Number(env.LIMIT)) } };`

// FetchPage GETs a URL: status, headers, truncated body.
func (r *Runner) FetchPage(ctx context.Context, rawURL string) (*RunResult, error) {
	if strings.TrimSpace(rawURL) == "" {
		return nil, ErrURLRequired
	}

	return r.Run(ctx, fetchScript, map[string]string{
		"URL": rawURL, "LIMIT": fmt.Sprint(fetchBodyLimit),
	}, nil, 0)
}

// snapshotExpression walks the DOM and renders the interactive and
// structural elements, accessibility-tree style: role, accessible name and a
// selector the script can use. It runs inside the page through
// page.evaluate, the existing browser runtime's escape hatch.
const snapshotExpression = `(function () {
  var out = [];
  var keep = { a:1, button:1, input:1, select:1, textarea:1, label:1, form:1, nav:1, main:1,
    h1:1, h2:1, h3:1, h4:1, table:1, img:1, li:1 };
  function selector(el) {
    var tag = el.tagName.toLowerCase();
    if (el.id) return tag + "#" + el.id;
    var name = el.getAttribute("name");
    if (name) return tag + "[name=\"" + name + "\"]";
    var testid = el.getAttribute("data-testid");
    if (testid) return "[data-testid=\"" + testid + "\"]";
    if (el.classList && el.classList.length) return tag + "." + el.classList[0];
    return tag;
  }
  function walk(el, depth) {
    if (out.length > 600) return;
    var tag = el.tagName.toLowerCase();
    if (tag === "script" || tag === "style" || tag === "noscript") return;
    var role = el.getAttribute("role") || "";
    var interesting = keep[tag] || role;
    if (interesting) {
      var name = el.getAttribute("aria-label") || el.getAttribute("placeholder") ||
        el.getAttribute("alt") || el.innerText || el.value || "";
      name = String(name).replace(/\s+/g, " ").trim().slice(0, 80);
      var type = el.getAttribute("type");
      out.push(new Array(Math.min(depth, 8) + 1).join("  ") + (role || tag) +
        (name ? " \"" + name + "\"" : "") + " " + selector(el) + (type ? " type=" + type : ""));
    }
    for (var i = 0; i < el.children.length; i++) walk(el.children[i], depth + (interesting ? 1 : 0));
  }
  if (document.body) walk(document.body, 0);
  return { title: document.title, url: location.href, tree: out.join("\n") };
})()`

const snapshotScript = `var page = browser.open();
var nav = page.goto(env.URL);
if (!nav.ok) { return { status: "down", output: { error: nav.error } }; }
var snap = page.evaluate(env.EXPR);
if (!snap.ok) { return { status: "down", output: { error: snap.error } }; }
var v = snap.value || {};
return { status: "up", output: { url: v.url, title: v.title,
  tree: String(v.tree || "").substring(0, Number(env.LIMIT)) } };`

// BrowserSnapshot opens a URL in the browser runtime and returns its
// accessibility-style tree.
func (r *Runner) BrowserSnapshot(ctx context.Context, rawURL string) (*RunResult, error) {
	if strings.TrimSpace(rawURL) == "" {
		return nil, ErrURLRequired
	}

	return r.Run(ctx, snapshotScript, map[string]string{
		"URL": rawURL, "EXPR": snapshotExpression, "LIMIT": fmt.Sprint(snapshotLimit),
	}, nil, 0)
}

// scrubResult replaces every secret value in the result's strings.
func scrubResult(result *RunResult, secrets map[string]string) {
	if len(secrets) == 0 {
		return
	}

	// Longest first, so a secret containing another is replaced whole.
	names := make([]string, 0, len(secrets))
	for name, value := range secrets {
		if value != "" {
			names = append(names, name)
		}
	}

	sort.Slice(names, func(i, j int) bool { return len(secrets[names[i]]) > len(secrets[names[j]]) })

	pairs := make([]string, 0, len(names)*2)
	for _, name := range names {
		pairs = append(pairs, secrets[name], fmt.Sprintf(scrubPlaceholder, name))
	}

	if len(pairs) == 0 {
		return
	}

	replacer := strings.NewReplacer(pairs...)

	if scrubbed, ok := scrubValue(map[string]any(result.Output), replacer).(map[string]any); ok {
		result.Output = scrubbed
	}

	if scrubbed, ok := scrubValue(map[string]any(result.Metrics), replacer).(map[string]any); ok {
		result.Metrics = scrubbed
	}
}

func scrubValue(value any, replacer *strings.Replacer) any {
	switch typed := value.(type) {
	case string:
		return replacer.Replace(typed)
	case map[string]any:
		if typed == nil {
			return typed
		}

		out := make(map[string]any, len(typed))
		for key, inner := range typed {
			out[replacer.Replace(key)] = scrubValue(inner, replacer)
		}

		return out
	case []any:
		out := make([]any, len(typed))
		for i, inner := range typed {
			out[i] = scrubValue(inner, replacer)
		}

		return out
	default:
		return value
	}
}

// forModel renders a value as bounded JSON for a tool result.
func forModel(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "error: " + err.Error()
	}

	if len(raw) > modelOutputLimit {
		return string(raw[:modelOutputLimit]) + "...(truncated)"
	}

	return string(raw)
}
