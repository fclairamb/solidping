package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/aichecks"
	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/testruns"
)

// js check authoring probes (spec 2026-10-03-07): the same tools the
// server-side generator gives its LLM, so any external agent can author a
// `js` check without SolidPing calling an LLM.
//
// They write nothing in SolidPing, but they execute on the API node, so they
// are admitted like the REST test run (spec 2026-11-19-03): one run per org
// at a time under the process cap, 20 per org per minute, and their browsers
// count against checkbrowser.MaxTestRunBrowsers. run_js_script and
// browser_snapshot execute arbitrary code or hold a browser, so they need a
// write-capable token and the `user` role, like POST …/checks/test-run;
// fetch_page stays callable with mcp:read.

// probeRunner is what the probes call. *aichecks.Runner implements it.
type probeRunner interface {
	Run(
		ctx context.Context, script string, env, secrets map[string]string, allowed []string, timeout time.Duration,
	) (*aichecks.RunResult, error)
	FetchPage(ctx context.Context, rawURL string) (*aichecks.RunResult, error)
	BrowserSnapshot(ctx context.Context, rawURL string) (*aichecks.RunResult, error)
}

const (
	propScript  = "script"
	propEnv     = "env"
	propSecrets = "secrets"
	propURL     = "url"
	propTimeout = "timeout"
	// propDurationMs is the run duration key of a probe result.
	propDurationMs = "durationMs"
)

// defaultScriptRunner runs probes behind the config's egress policy.
func defaultScriptRunner(cfg *config.Config) *aichecks.Runner {
	guard := egress.New(cfg != nil && cfg.EgressAllowsPrivateTargets())

	return &aichecks.Runner{Guard: func() *egress.Guard { return guard }}
}

// runner returns the probe runner, defaulting to one behind the strictest
// egress policy when the handler was built without NewHandler.
func (h *Handler) runner() probeRunner {
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

// SetProbeLimiter shares the process-wide test-run limiter with the probes,
// so a REST test run and an MCP probe of one org count against the same caps.
func (h *Handler) SetProbeLimiter(limiter *testruns.Limiter) {
	if limiter != nil {
		h.probeLimiter = limiter
	}
}

func (h *Handler) limiter() *testruns.Limiter {
	h.probeLimiterOnce.Do(func() {
		if h.probeLimiter == nil {
			h.probeLimiter = testruns.New()
		}
	})

	return h.probeLimiter
}

// probeOrgKey is the limiter key of the token's org: its UID, the key the
// REST test run uses, so both count against one budget. Without a database
// (unit tests) the slug stands in.
func (h *Handler) probeOrgKey(ctx context.Context, orgSlug string) (string, error) {
	if h.dbService == nil {
		return orgSlug, nil
	}

	org, err := h.dbService.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil || org == nil {
		return "", errProbeOrgUnknown
	}

	return org.UID, nil
}

var errProbeOrgUnknown = errors.New("organization not found")

// probeRetryHint is the "retry in N s" tail of a refusal.
func probeRetryHint() string {
	return fmt.Sprintf("retry in %d s", int(testruns.RetryAfter/time.Second))
}

// admitProbe applies the per-org rate and the concurrency caps before a
// probe runs. On success it returns the context to run with (its browsers
// count against the test-run budget) and the release to defer; on refusal,
// the tool error to return.
func (h *Handler) admitProbe(
	ctx context.Context, orgSlug string,
) (context.Context, func(), *ToolCallResult) {
	key, err := h.probeOrgKey(ctx, orgSlug)
	if err != nil {
		res := errorResult(err.Error())

		return nil, nil, &res
	}

	limiter := h.limiter()

	if !limiter.Allow(key) {
		res := errorResult(fmt.Sprintf(
			"too many runs: this organization is limited to %d script runs per minute, %s",
			testruns.RunsPerMinute, probeRetryHint()))

		return nil, nil, &res
	}

	release, err := limiter.Acquire(key)
	if err != nil {
		msg := "too many runs in progress, " + probeRetryHint()
		if errors.Is(err, testruns.ErrOrgBusy) {
			msg = fmt.Sprintf("too many runs in progress: this organization already has a run in progress "+
				"(limit: %d at a time per organization), %s", testruns.MaxPerOrg, probeRetryHint())
		}

		res := errorResult(msg)

		return nil, nil, &res
	}

	return checkbrowser.WithTestRunBudget(ctx), release, nil
}

func runResultOutputSchema() map[string]any {
	return objectSchema(map[string]any{
		"status":       stringProp("up, down, timeout or error, as a SolidPing worker would report it."),
		propDurationMs: intProp("Run duration in milliseconds."),
		"output":       objectProp("The script's output object, with console output under `console`."),
		"metrics":      objectProp("The script's metrics."),
	}, []string{"status"})
}

func runJSScriptDef() ToolDefinition {
	return ToolDefinition{
		Name: toolRunJSScript,
		Description: "Run a `js` check script once, exactly as a SolidPing worker would, and return its " +
			"result (status, output with console, metrics) without saving anything. Use it to author " +
			"and test a js check before create_check. `env` carries plaintext parameters; `secrets` " +
			"only NAMES the secret parameters the script reads (secrets.NAME): they run empty, never " +
			"with real values. It executes code on the server, so it needs a write-capable token (mcp scope) " +
			"and the user role, like the dashboard's test run. One run per organization at a time, " +
			"20 per minute.",
		InputSchema: objectSchema(map[string]any{
			propScript: withMaxLength(
				stringProp("The js check script (a function body that returns {status, ...})."), maxLongStringLength),
			propEnv:     objectProp("Plaintext parameters exposed as env.NAME, e.g. {\"BASE_URL\": \"https://acme.com\"}."),
			propSecrets: arrayOfStringsProp("Names of the secret parameters the script reads; they run empty."),
			propTimeout: stringProp("Run timeout as a duration string (default and max 30s)."),
		}, []string{propScript}),
		OutputSchema: runResultOutputSchema(),
		Annotations:  execProbeAnnotations("Run js script"),
	}
}

func fetchPageDef() ToolDefinition {
	return ToolDefinition{
		Name: toolFetchPage,
		Description: "HTTP GET a URL from the SolidPing server and return its status code, headers and " +
			"the first 16 KB of the body, to explore a target before writing a js check. Read-only: " +
			"works with mcp:read tokens. One run per organization at a time, 20 per minute.",
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
			"check's browser steps. It holds a server browser, so it needs a write-capable token (mcp " +
			"scope) and the user role. One run per organization at a time, 20 per minute.",
		InputSchema:  objectSchema(map[string]any{propURL: stringProp("Absolute http(s) URL.")}, []string{propURL}),
		OutputSchema: runResultOutputSchema(),
		Annotations:  execProbeAnnotations("Browser snapshot"),
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

func (h *Handler) toolRunJSScript(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
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

	ctx, release, refused := h.admitProbe(ctx, orgSlug)
	if refused != nil {
		return *refused
	}
	defer release()

	result, err := h.runner().Run(ctx, script, stringMapArg(args, propEnv), secrets, nil, timeout)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(result)
}

func (h *Handler) toolFetchPage(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	ctx, release, refused := h.admitProbe(ctx, orgSlug)
	if refused != nil {
		return *refused
	}
	defer release()

	result, err := h.runner().FetchPage(ctx, getStringArg(args, propURL))
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(result)
}

func (h *Handler) toolBrowserSnapshot(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	ctx, release, refused := h.admitProbe(ctx, orgSlug)
	if refused != nil {
		return *refused
	}
	defer release()

	result, err := h.runner().BrowserSnapshot(ctx, getStringArg(args, propURL))
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(result)
}
