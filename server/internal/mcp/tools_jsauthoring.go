package mcp

import (
	"context"
	"time"

	"github.com/fclairamb/solidping/server/internal/aichecks"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/egress"
)

// js check authoring probes (spec 2026-10-03-07): the same tools the
// server-side generator gives its LLM, so any external agent can author a
// `js` check without SolidPing calling an LLM. Same scope rules as
// validate_check: they write nothing, so mcp:read tokens may call them.

const (
	propScript  = "script"
	propEnv     = "env"
	propSecrets = "secrets"
	propURL     = "url"
	propTimeout = "timeout"
)

// defaultScriptRunner runs probes behind the config's egress policy.
func defaultScriptRunner(cfg *config.Config) *aichecks.Runner {
	guard := egress.New(cfg != nil && cfg.EgressAllowsPrivateTargets())

	return &aichecks.Runner{Guard: func() *egress.Guard { return guard }}
}

// runner returns the probe runner, defaulting to one behind the strictest
// egress policy when the handler was built without NewHandler.
func (h *Handler) runner() *aichecks.Runner {
	if h.scriptRunner == nil {
		return defaultScriptRunner(nil)
	}

	return h.scriptRunner
}

// SetScriptRunner replaces the runner of the js authoring probes.
func (h *Handler) SetScriptRunner(runner *aichecks.Runner) {
	if runner != nil {
		h.scriptRunner = runner
	}
}

func runResultOutputSchema() map[string]any {
	return objectSchema(map[string]any{
		"status":     stringProp("up, down, timeout or error, as a SolidPing worker would report it."),
		"durationMs": intProp("Run duration in milliseconds."),
		"output":     objectProp("The script's output object, with console output under `console`."),
		"metrics":    objectProp("The script's metrics."),
	}, []string{"status"})
}

func runJSScriptDef() ToolDefinition {
	return ToolDefinition{
		Name: toolRunJSScript,
		Description: "Run a `js` check script once, exactly as a SolidPing worker would, and return its " +
			"result (status, output with console, metrics) without saving anything. Use it to author " +
			"and test a js check before create_check. `env` carries plaintext parameters; `secrets` " +
			"only NAMES the secret parameters the script reads (secrets.NAME): they run empty, never " +
			"with real values. Read-only: works with mcp:read tokens.",
		InputSchema: objectSchema(map[string]any{
			propScript:  stringProp("The js check script (a function body that returns {status, ...})."),
			propEnv:     objectProp("Plaintext parameters exposed as env.NAME, e.g. {\"BASE_URL\": \"https://acme.com\"}."),
			propSecrets: arrayOfStringsProp("Names of the secret parameters the script reads; they run empty."),
			propTimeout: stringProp("Run timeout as a duration string (default and max 30s)."),
		}, []string{propScript}),
		OutputSchema: runResultOutputSchema(),
		Annotations:  probeAnnotations("Run js script"),
	}
}

func fetchPageDef() ToolDefinition {
	return ToolDefinition{
		Name: toolFetchPage,
		Description: "HTTP GET a URL from the SolidPing server and return its status code, headers and " +
			"the first 16 KB of the body, to explore a target before writing a js check. Read-only: " +
			"works with mcp:read tokens.",
		InputSchema:  objectSchema(map[string]any{propURL: stringProp("Absolute http(s) URL.")}, []string{propURL}),
		OutputSchema: runResultOutputSchema(),
		Annotations:  probeAnnotations("Fetch page"),
	}
}

func browserSnapshotDef() ToolDefinition {
	return ToolDefinition{
		Name: toolBrowserSnapshot,
		Description: "Open a URL in the server's headless browser and return its accessibility-style " +
			"tree (role, accessible name and a CSS selector per element), to pick selectors for a js " +
			"check's browser steps. Read-only: works with mcp:read tokens.",
		InputSchema:  objectSchema(map[string]any{propURL: stringProp("Absolute http(s) URL.")}, []string{propURL}),
		OutputSchema: runResultOutputSchema(),
		Annotations:  probeAnnotations("Browser snapshot"),
	}
}

func stringMapArg(args map[string]any, key string) map[string]string {
	raw := getMapArg(args, key)
	out := make(map[string]string, len(raw))

	for name, value := range raw {
		if str, ok := value.(string); ok {
			out[name] = str
		}
	}

	return out
}

func (h *Handler) toolRunJSScript(ctx context.Context, _ string, args map[string]any) ToolCallResult {
	script := getStringArg(args, propScript)
	if script == "" {
		return errorResult("script is required")
	}

	var timeout time.Duration

	if raw := getStringArg(args, propTimeout); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return errorResult("timeout must be a duration string, e.g. \"10s\"")
		}

		timeout = parsed
	}

	secrets := map[string]string{}
	for _, name := range getStringSliceArg(args, propSecrets) {
		secrets[name] = ""
	}

	result, err := h.runner().Run(ctx, script, stringMapArg(args, propEnv), secrets, timeout)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(result)
}

func (h *Handler) toolFetchPage(ctx context.Context, _ string, args map[string]any) ToolCallResult {
	result, err := h.runner().FetchPage(ctx, getStringArg(args, propURL))
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(result)
}

func (h *Handler) toolBrowserSnapshot(ctx context.Context, _ string, args map[string]any) ToolCallResult {
	result, err := h.runner().BrowserSnapshot(ctx, getStringArg(args, propURL))
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(result)
}
