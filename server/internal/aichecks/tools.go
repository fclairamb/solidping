package aichecks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/fclairamb/solidping/server/internal/ai"
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

	last *scriptRun
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

			if strings.TrimSpace(args.Script) == "" {
				return "", errScriptRequired
			}

			secrets, note, err := rec.prepare(args.Script)
			if err != nil {
				return "", err
			}

			res, err := rec.runner.Run(ctx, args.Script, rec.env, secrets, 0)
			if err != nil {
				return "", err
			}

			run := &scriptRun{Script: args.Script, Result: res, Final: args.Final}
			rec.last = run

			if res.Up() {
				if rec.ups == nil {
					rec.ups = map[string]*scriptRun{}
				}

				rec.ups[strings.TrimSpace(args.Script)] = run

				if args.Final {
					rec.lastUp = run
				}
			}

			payload := map[string]any{"result": res}
			if note != "" {
				payload["note"] = note
			}

			return forModel(payload), nil
		},
	}
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
