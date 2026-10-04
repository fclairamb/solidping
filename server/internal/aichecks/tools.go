package aichecks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/fclairamb/solidping/server/internal/ai"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Tool names, shared with the MCP server.
const (
	ToolRunScript       = "run_script"
	ToolFetchPage       = "fetch_page"
	ToolBrowserSnapshot = "browser_snapshot"
)

const (
	schemaType       = "type"
	schemaObject     = "object"
	schemaProperties = "properties"
	schemaRequired   = "required"
	schemaDesc       = "description"
)

var errScriptRequired = errors.New("script is required")

type urlArgs struct {
	URL string `json:"url"`
}

type scriptArgs struct {
	Script string `json:"script"`
	// Final marks the finished check script. Only a final run that passes
	// ends the loop and is kept: an exploratory probe that returns "up"
	// asserts nothing and must never become the check.
	Final bool `json:"final"`
}

func urlSchema(desc string) map[string]any {
	return map[string]any{
		schemaType:       schemaObject,
		schemaProperties: map[string]any{"url": map[string]any{schemaType: "string", schemaDesc: desc}},
		schemaRequired:   []string{"url"},
	}
}

// fetchPageTool is the fetch_page tool.
func (r *Runner) fetchPageTool() ai.Tool {
	return ai.Tool{
		Def: ai.ToolDef{
			Name:        ToolFetchPage,
			Description: "HTTP GET a URL. Returns its status code, headers and the first 16 KB of the body.",
			Parameters:  urlSchema("Absolute http(s) URL."),
		},
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args urlArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}

			res, err := r.FetchPage(ctx, args.URL)
			if err != nil {
				return "", err
			}

			return forModel(res), nil
		},
	}
}

// browserSnapshotTool is the browser_snapshot tool.
func (r *Runner) browserSnapshotTool() ai.Tool {
	return ai.Tool{
		Def: ai.ToolDef{
			Name: ToolBrowserSnapshot,
			Description: "Open a URL in a real headless browser and return its accessibility-style tree " +
				"(role, accessible name and a CSS selector per interactive or structural element).",
			Parameters: urlSchema("Absolute http(s) URL."),
		},
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args urlArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}

			res, err := r.BrowserSnapshot(ctx, args.URL)
			if err != nil {
				return "", err
			}

			return forModel(res), nil
		},
	}
}

// scriptRun is one run_script call of a loop.
type scriptRun struct {
	Script string
	Result *RunResult
	Final  bool
}

// runRecorder is the run_script tool of one loop: it runs the script, and
// remembers the last run and the last passing one.
type runRecorder struct {
	runner *Runner
	// prepare decides how a script runs: the secrets it gets, a note for the
	// model, or a refusal (an error the model sees instead of a run).
	prepare func(script string) (map[string]string, string, error)
	env     map[string]string
	// progress receives every run, for the dashboard. Nil drops them.
	progress func(Progress)
	nudges   int
	// invitedToStop is set once the model was told it may stop and explain.
	invitedToStop bool

	last *scriptRun
	// lastFinal is the last run marked final, passing or not.
	lastFinal *scriptRun
	// lastUp is the last passing run marked final.
	lastUp *scriptRun
	// ups are the passing runs by script, final or not, for passing().
	ups map[string]*scriptRun
}

func (rec *runRecorder) tool() ai.Tool {
	return ai.Tool{
		Def: ai.ToolDef{
			Name: ToolRunScript,
			Description: "Run a js check script exactly as a SolidPing worker would, with the check's env " +
				"and secrets. Returns status, output (with console) and metrics. Secret values are " +
				"never shown: they appear as [secret:NAME]. Set final to true only for the finished " +
				"check script that implements the whole contract; a final run returning \"up\" ends the work.",
			Parameters: map[string]any{
				schemaType: schemaObject,
				schemaProperties: map[string]any{
					"script": map[string]any{schemaType: "string", schemaDesc: "The full js check script."},
					"final": map[string]any{
						schemaType: "boolean",
						schemaDesc: "true for the finished check script, false for an exploratory probe " +
							"(a probe is never saved, even when it returns \"up\").",
					},
				},
				schemaRequired: []string{"script", "final"},
			},
		},
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args scriptArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}

			return rec.run(ctx, args.Script, args.Final)
		},
	}
}

// run runs one script for the model and returns the tool result it reads.
func (rec *runRecorder) run(ctx context.Context, script string, final bool) (string, error) {
	if strings.TrimSpace(script) == "" {
		return "", errScriptRequired
	}

	rec.emit(&Progress{Type: ProgressTool, Tool: ToolRunScript, Final: final})

	out, err := rec.runScript(ctx, script, final)
	if err != nil {
		rec.emit(&Progress{Type: ProgressToolResult, Tool: ToolRunScript, Final: final, Error: err.Error()})

		return "", err
	}

	res := rec.last.Result
	rec.emit(&Progress{
		Type: ProgressToolResult, Tool: ToolRunScript, Final: final,
		Status: res.Status, Detail: runSummary(res), DurationMs: res.DurationMs,
	})

	return out, nil
}

func (rec *runRecorder) runScript(ctx context.Context, script string, final bool) (string, error) {
	secrets, note, err := rec.prepare(script)
	if err != nil {
		return "", err
	}

	res, err := rec.runner.Run(ctx, script, rec.env, secrets, 0)
	if err != nil {
		// A script that cannot run (a syntax error) is still an attempt the
		// user should see when nothing passes.
		rec.record(&scriptRun{
			Script: script, Final: final,
			Result: &RunResult{Status: checkerdef.StatusError.String(), Output: map[string]any{outputKeyError: err.Error()}},
		})

		return "", err
	}

	run := &scriptRun{Script: script, Result: res, Final: final}
	rec.record(run)

	if res.Up() {
		if rec.ups == nil {
			rec.ups = map[string]*scriptRun{}
		}

		rec.ups[strings.TrimSpace(script)] = run

		if final {
			rec.lastUp = run
		}
	}

	payload := map[string]any{"result": res}
	if note != "" {
		payload["note"] = note
	}

	return forModel(payload), nil
}

func (rec *runRecorder) record(run *scriptRun) {
	rec.last = run
	if run.Final {
		rec.lastFinal = run
	}
}

// attempt is the run to show when nothing passed: the last final one, else
// the last probe.
func (rec *runRecorder) attempt() *scriptRun {
	if rec.lastFinal != nil {
		return rec.lastFinal
	}

	return rec.last
}

func (rec *runRecorder) emit(event *Progress) {
	if rec.progress != nil {
		rec.progress(*event)
	}
}

// maxNudges bounds how many times a model that stopped without a passing
// final script is sent back to work.
const maxNudges = 2

// nudge is the loop's answer to a model that stopped talking without a
// passing final run. Weaker models often answer with the finished script in a
// fenced block instead of testing it, or rewrite it slightly after a passing
// probe: that script is run as final here, and kept when it passes. Otherwise
// the model is told why it is not done yet, at most maxNudges times. An empty
// answer ends the loop.
func (rec *runRecorder) nudge(ctx context.Context, text string) string {
	if rec.done() || rec.nudges >= maxNudges {
		return ""
	}

	rec.nudges++

	if script := fencedScript(text); script != "" && rec.ups[script] == nil {
		out, err := rec.run(ctx, script, true)
		if err != nil {
			return "The script in your answer could not run: " + err.Error() +
				". Fix it and test it with run_script, final: true."
		}

		if rec.done() {
			return ""
		}

		return "The script in your answer was tested as final and did not pass:\n" + out +
			"\nFix it and test it with run_script, final: true."
	}

	if script := fencedScript(text); script != "" || rec.invitedToStop {
		// A passing probe handed back as the answer (passing() accepts it),
		// or a model that already had its chance to say what fails.
		return ""
	}

	rec.invitedToStop = true

	return "No final script has passed yet. Write the whole check and test it with run_script, " +
		"final: true. If the target cannot satisfy the contract (wrong credentials, page missing, " +
		"service down), answer with one sentence saying what fails, without calling a tool."
}

func (rec *runRecorder) done() bool {
	return rec.last != nil && rec.last.Final && rec.last.Result.Up()
}

// passing is the script the loop produced: the last passing final run, or,
// when the model never set final, the passing run of the script its last
// answer hands back in a fenced code block (the protocol of the rules). Nil
// when neither exists: a probe that returned "up" is never the check.
func (rec *runRecorder) passing(result *ai.LoopResult) *scriptRun {
	if rec.lastUp != nil {
		return rec.lastUp
	}

	if result == nil {
		return nil
	}

	script := fencedScript(result.Text)
	if script == "" {
		return nil
	}

	return rec.ups[script]
}

// fencedScript is the content of the last fenced code block of text, trimmed.
func fencedScript(text string) string {
	end := strings.LastIndex(text, "```")
	if end < 0 {
		return ""
	}

	start := strings.LastIndex(text[:end], "```")
	if start < 0 {
		return ""
	}

	block := text[start+3 : end]
	// Drop the language tag line (```js).
	if nl := strings.IndexByte(block, '\n'); nl >= 0 && !strings.ContainsAny(block[:nl], " ;({=") {
		block = block[nl+1:]
	}

	return strings.TrimSpace(block)
}

// explanationLimit bounds the model's last message carried by a failure.
const explanationLimit = 2000

var fenceRE = regexp.MustCompile("(?s)```.*?(```|$)")

// withoutFences drops the fenced code blocks of a model message, keeping its
// prose.
func withoutFences(text string) string {
	return strings.TrimSpace(fenceRE.ReplaceAllString(text, ""))
}

// secretNames lists the keys of a secrets map, as the model sees them.
func secretNames(secrets map[string]string) []string {
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}

	return names
}

// blankSecrets maps every secret name to an empty value.
func blankSecrets(secrets map[string]string) map[string]string {
	out := make(map[string]string, len(secrets))
	for name := range secrets {
		out[name] = ""
	}

	return out
}
