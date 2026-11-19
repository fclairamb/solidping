package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/aichecks"
	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/checktestrun"
	"github.com/fclairamb/solidping/server/internal/testruns"
)

// probeEnv is two orgs, each with a `user` member, and a viewer in the
// first, wired into a Handler backed by a real database so the role gate of
// the code-running probes can pass.
type probeEnv struct {
	handler      *Handler
	orgA, orgB   *models.Organization
	userA, userB *models.User
	viewerA      *models.User
}

func newProbeEnv(t *testing.T) *probeEnv {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	mkOrg := func(slug string) *models.Organization {
		org := models.NewOrganization(slug, slug)
		r.NoError(dbSvc.CreateOrganization(ctx, org))

		return org
	}

	mkMember := func(org *models.Organization, email string, role models.MemberRole) *models.User {
		user := models.NewUser(email)
		r.NoError(dbSvc.CreateUser(ctx, user))
		r.NoError(dbSvc.CreateOrganizationMember(ctx, models.NewOrganizationMember(org.UID, user.UID, role)))

		return user
	}

	env := &probeEnv{orgA: mkOrg("acme-a"), orgB: mkOrg("acme-b")}
	env.userA = mkMember(env.orgA, "user@acme-a.example", models.MemberRoleUser)
	env.userB = mkMember(env.orgB, "user@acme-b.example", models.MemberRoleUser)
	env.viewerA = mkMember(env.orgA, "viewer@acme-a.example", models.MemberRoleViewer)

	env.handler = &Handler{dbService: dbSvc}
	env.handler.registerTools()

	return env
}

func (e *probeEnv) claims(org *models.Organization, user *models.User, scopes ...string) *auth.Claims {
	return &auth.Claims{UserUID: user.UID, OrgSlug: org.Slug, Scopes: scopes}
}

func jsAuthoringHandler(t *testing.T) (*Handler, string) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Pinned so two calls straddling a second boundary compare equal.
		w.Header().Set("Date", "Mon, 02 Nov 2026 00:00:00 GMT")
		_, _ = w.Write([]byte(`{"projects":[1]}`))
	}))
	t.Cleanup(srv.Close)

	handler := newTestHandler()
	// The test target is on loopback, which the default egress policy refuses.
	handler.SetScriptRunner(&aichecks.Runner{})

	return handler, srv.URL
}

func toolCall(t *testing.T, name string, args map[string]any) string {
	t.Helper()

	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	require.NoError(t, err)

	return string(raw)
}

// TestRunJSScriptReturnsTheCheckerResult: the tool runs the script through
// the js checker and hands its result back to a `user` with a write-capable
// token.
func TestRunJSScriptReturnsTheCheckerResult(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newProbeEnv(t)
	env.handler.SetScriptRunner(&aichecks.Runner{})
	_, url := jsAuthoringHandler(t)

	body := toolCall(t, toolRunJSScript, map[string]any{
		"script": `var r = http.get(env.BASE_URL); var d = JSON.parse(r.body);
console.log("token is", secrets.TOKEN === "" ? "blank" : "set");
return { status: d.projects.length > 0 ? "up" : "down", metrics: { n: d.projects.length } };`,
		"env":     map[string]any{"BASE_URL": url},
		"secrets": []any{"TOKEN"},
	})
	rec, req := makeRequest(t, http.MethodPost, body, env.claims(env.orgA, env.userA, scopeMCP))
	r.NoError(env.handler.Handle(rec, req))
	r.Equal(http.StatusOK, rec.Code)

	resp := decodeResponse(t, rec)
	r.Nil(resp.Error)

	var result struct {
		IsError           bool               `json:"isError"`
		StructuredContent aichecks.RunResult `json:"structuredContent"`
	}

	raw, err := json.Marshal(resp.Result)
	r.NoError(err)
	r.NoError(json.Unmarshal(raw, &result))
	r.False(result.IsError)
	r.Equal("up", result.StructuredContent.Status)
	r.InDelta(1, result.StructuredContent.Metrics["n"], 0)
	r.Contains(result.StructuredContent.Output["console"], "token is blank", "secrets are names only")
}

func TestFetchPageTool(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	handler, url := jsAuthoringHandler(t)

	rec, req := makeRequest(t, http.MethodPost, toolCall(t, toolFetchPage, map[string]any{"url": url}),
		claimsWithScopes("mcp"))
	r.NoError(handler.Handle(rec, req))

	resp := decodeResponse(t, rec)
	r.Nil(resp.Error)

	raw, err := json.Marshal(resp.Result)
	r.NoError(err)
	r.Contains(string(raw), `"statusCode":200`)
	r.Contains(string(raw), `projects`)

	// Missing URL: a tool error, not a crash.
	rec, req = makeRequest(t, http.MethodPost, toolCall(t, toolFetchPage, map[string]any{}), claimsWithScopes("mcp"))
	r.NoError(handler.Handle(rec, req))

	raw, err = json.Marshal(decodeResponse(t, rec).Result)
	r.NoError(err)
	r.Contains(string(raw), `"isError":true`)
}

// TestJSAuthoringToolsRefuseUnauthorizedTokens: a token without an MCP scope
// is refused before any tool runs.
func TestJSAuthoringToolsRefuseUnauthorizedTokens(t *testing.T) {
	t.Parallel()

	for _, name := range []string{toolRunJSScript, toolFetchPage, toolBrowserSnapshot} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			handler, url := jsAuthoringHandler(t)

			body := toolCall(t, name, map[string]any{"script": `return {status:"up"};`, "url": url})
			rec, req := makeRequest(t, http.MethodPost, body, claimsWithScopes("checks:read"))
			r.NoError(handler.Handle(rec, req))
			r.Equal(http.StatusForbidden, rec.Code)

			rec, req = makeRequest(t, http.MethodPost, body, nil)
			r.NoError(handler.Handle(rec, req))
			r.NotEqual(http.StatusOK, rec.Code)
		})
	}
}

// TestJSAuthoringToolsAccess: fetch_page reads, like validate_check; the
// probes that run code or a browser take the write gates (spec 2026-11-19-03).
func TestJSAuthoringToolsAccess(t *testing.T) {
	t.Parallel()

	for _, name := range []string{toolFetchPage, toolValidateCheck} {
		require.False(t, toolMutates(name, map[string]any{}), name)
	}

	for _, name := range []string{toolRunJSScript, toolBrowserSnapshot} {
		require.True(t, toolMutates(name, map[string]any{}), name)
	}
}

// TestDefaultRunnerRefusesPrivateTargets: without the server's runner the
// probes go through the strict egress policy (no SSRF to loopback).
func TestDefaultRunnerRefusesPrivateTargets(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, url := jsAuthoringHandler(t)
	handler := newTestHandler()

	rec, req := makeRequest(t, http.MethodPost, toolCall(t, toolFetchPage, map[string]any{"url": url}),
		claimsWithScopes("mcp"))
	r.NoError(handler.Handle(rec, req))

	raw, err := json.Marshal(decodeResponse(t, rec).Result)
	r.NoError(err)
	r.NotContains(string(raw), "projects")
	r.Contains(string(raw), `"status":"down"`)
}

// blockingProbeRunner is a probe runner (and a REST test-run runner) that
// reports each start, records whether its context carries the test-run
// browser budget, and blocks until release is closed.
type blockingProbeRunner struct {
	started chan bool
	release chan struct{}
}

func newBlockingProbeRunner() *blockingProbeRunner {
	return &blockingProbeRunner{started: make(chan bool, 16), release: make(chan struct{})}
}

func (b *blockingProbeRunner) block(ctx context.Context) (*aichecks.RunResult, error) {
	b.started <- checkbrowser.IsTestRun(ctx)

	select {
	case <-b.release:
	case <-ctx.Done():
	}

	return &aichecks.RunResult{Status: "up"}, nil
}

func (b *blockingProbeRunner) Run(
	ctx context.Context, _ string, _, _ map[string]string, _ []string, _ time.Duration,
) (*aichecks.RunResult, error) {
	return b.block(ctx)
}

func (b *blockingProbeRunner) FetchPage(ctx context.Context, _ string) (*aichecks.RunResult, error) {
	return b.block(ctx)
}

func (b *blockingProbeRunner) BrowserSnapshot(ctx context.Context, _ string) (*aichecks.RunResult, error) {
	return b.block(ctx)
}

func (b *blockingProbeRunner) Test(
	ctx context.Context, _ string, _, _ map[string]string, _ time.Duration,
) (*aichecks.RunResult, error) {
	return b.block(ctx)
}

// instantProbeRunner answers every probe at once.
type instantProbeRunner struct{}

func (instantProbeRunner) Run(
	_ context.Context, _ string, _, _ map[string]string, _ []string, _ time.Duration,
) (*aichecks.RunResult, error) {
	return &aichecks.RunResult{Status: "up"}, nil
}

func (instantProbeRunner) FetchPage(context.Context, string) (*aichecks.RunResult, error) {
	return &aichecks.RunResult{Status: "up"}, nil
}

func (instantProbeRunner) BrowserSnapshot(context.Context, string) (*aichecks.RunResult, error) {
	return &aichecks.RunResult{Status: "up"}, nil
}

// probeOutcome is what one tools/call answered.
type probeOutcome struct {
	rpcErr  *RPCError
	isError bool
	text    string
}

// callProbe drives one tools/call through Handle. It asserts nothing, so
// goroutines can call it.
func (e *probeEnv) callProbe(ctx context.Context, claims *auth.Claims, tool string) probeOutcome {
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": map[string]any{
			propScript: `return {status:"up"};`, propURL: "https://acme.example",
		}},
	})

	httpReq := httptest.NewRequestWithContext(
		context.WithValue(ctx, base.ContextKeyClaims, claims), http.MethodPost, "/api/v1/mcp", bytes.NewReader(raw))
	httpReq.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	if err := e.handler.Handle(rec, httpReq); err != nil {
		return probeOutcome{isError: true, text: err.Error()}
	}

	var resp Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return probeOutcome{isError: true, text: err.Error()}
	}

	if resp.Error != nil {
		return probeOutcome{rpcErr: resp.Error}
	}

	var result ToolCallResult

	body, _ := json.Marshal(resp.Result)
	_ = json.Unmarshal(body, &result)

	out := probeOutcome{isError: result.IsError}
	if len(result.Content) > 0 {
		out.text = result.Content[0].Text
	}

	return out
}

func waitStarted(t *testing.T, runner *blockingProbeRunner) bool {
	t.Helper()

	select {
	case marked := <-runner.started:
		return marked
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the probe never started")

		return false
	}
}

// TestProbesRunOneAtATimePerOrg is the regression test of spec 2026-11-19-03:
// a second run_js_script of the same org is refused while the first is in
// flight, and another org still runs.
func TestProbesRunOneAtATimePerOrg(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newProbeEnv(t)
	runner := newBlockingProbeRunner()
	env.handler.scriptRunner = runner

	userA := env.claims(env.orgA, env.userA, scopeMCP)
	firstA := make(chan probeOutcome, 1)

	go func() { firstA <- env.callProbe(t.Context(), userA, toolRunJSScript) }()

	r.True(waitStarted(t, runner), "the run counts against the test-run browser budget")

	for _, tool := range []string{toolRunJSScript, toolFetchPage, toolBrowserSnapshot} {
		second := env.callProbe(t.Context(), userA, tool)
		r.Nil(second.rpcErr, tool)
		r.True(second.isError, tool)
		r.Contains(second.text, "too many runs in progress", tool)
		r.Contains(second.text, "retry in 5 s", tool)
	}

	// Positive control: org B runs while org A's run is in flight.
	otherB := make(chan probeOutcome, 1)

	go func() {
		otherB <- env.callProbe(t.Context(), env.claims(env.orgB, env.userB, scopeMCP), toolRunJSScript)
	}()

	waitStarted(t, runner)
	close(runner.release)

	for _, done := range []chan probeOutcome{firstA, otherB} {
		outcome := <-done
		r.Nil(outcome.rpcErr)
		r.False(outcome.isError, outcome.text)
	}

	// Once its run is over, org A runs again.
	r.Zero(env.handler.limiter().BusyOrgs())
	again := env.callProbe(t.Context(), userA, toolRunJSScript)
	r.False(again.isError, again.text)
	<-runner.started
}

// TestProbeBrowsersCountAgainstTheTestRunBudget: every probe context is
// marked by checkbrowser.IsTestRun, so MCP browsers can never take more than
// checkbrowser.MaxTestRunBrowsers slots.
func TestProbeBrowsersCountAgainstTheTestRunBudget(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newProbeEnv(t)
	runner := newBlockingProbeRunner()
	close(runner.release)
	env.handler.scriptRunner = runner

	userA := env.claims(env.orgA, env.userA, scopeMCP)

	for _, tool := range []string{toolBrowserSnapshot, toolFetchPage, toolRunJSScript} {
		outcome := env.callProbe(t.Context(), userA, tool)
		r.False(outcome.isError, outcome.text)
		r.True(<-runner.started, tool)
	}
}

// TestProbesPerOrgRateLimit: 20 probes per org per minute, then a refusal;
// another org keeps its own allowance.
func TestProbesPerOrgRateLimit(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newProbeEnv(t)
	env.handler.scriptRunner = instantProbeRunner{}

	viewerRead := env.claims(env.orgA, env.viewerA, scopeMCPRead)

	for i := range testruns.RunsPerMinute {
		outcome := env.callProbe(t.Context(), viewerRead, toolFetchPage)
		r.Falsef(outcome.isError, "run %d: %s", i, outcome.text)
	}

	refused := env.callProbe(t.Context(), env.claims(env.orgA, env.userA, scopeMCP), toolRunJSScript)
	r.True(refused.isError)
	r.Contains(refused.text, "20 script runs per minute")

	other := env.callProbe(t.Context(), env.claims(env.orgB, env.userB, scopeMCP), toolRunJSScript)
	r.False(other.isError, other.text)
}

// TestCodeRunningProbesNeedAWriteToken: run_js_script and browser_snapshot
// (and probe with kind=script or browser) refuse a read-only token and a
// viewer, like the REST test run; fetch_page stays open to both.
func TestCodeRunningProbesNeedAWriteToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newProbeEnv(t)
	env.handler.scriptRunner = instantProbeRunner{}

	for _, tool := range []string{toolRunJSScript, toolBrowserSnapshot} {
		readOnly := env.callProbe(t.Context(), env.claims(env.orgA, env.userA, scopeMCPRead), tool)
		r.NotNil(readOnly.rpcErr, tool)
		r.Contains(readOnly.rpcErr.Message, "requires the mcp scope", tool)

		viewer := env.callProbe(t.Context(), env.claims(env.orgA, env.viewerA, scopeMCP), tool)
		r.NotNil(viewer.rpcErr, tool)
		r.Contains(viewer.rpcErr.Message, "requires the user role", tool)

		user := env.callProbe(t.Context(), env.claims(env.orgB, env.userB, scopeMCP), tool)
		r.Nil(user.rpcErr, tool)
		r.False(user.isError, user.text)
	}

	fetch := env.callProbe(t.Context(), env.claims(env.orgA, env.viewerA, scopeMCPRead), toolFetchPage)
	r.Nil(fetch.rpcErr)
	r.False(fetch.isError, fetch.text)
}

// TestRESTTestRunAndProbesShareTheLimiter: with the server's shared limiter,
// a REST test run of org A blocks an MCP probe of org A, not one of org B.
func TestRESTTestRunAndProbesShareTheLimiter(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newProbeEnv(t)
	runner := newBlockingProbeRunner()
	env.handler.scriptRunner = runner

	limiter := testruns.New()
	env.handler.SetProbeLimiter(limiter)
	rest := checktestrun.NewHandlerWithLimiter(runner, &config.Config{}, limiter)

	restDone := make(chan int, 1)

	go func() {
		ctx := context.WithValue(t.Context(), base.ContextKeyOrganization, env.orgA)
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/orgs/acme-a/checks/test-run",
			strings.NewReader(`{"type":"js","config":{"script":"return { status: 'up' };"}}`))
		rec := httptest.NewRecorder()
		_ = rest.TestRun(rec, req)
		restDone <- rec.Code
	}()

	waitStarted(t, runner)

	sameOrg := env.callProbe(t.Context(), env.claims(env.orgA, env.userA, scopeMCP), toolRunJSScript)
	r.True(sameOrg.isError)
	r.Contains(sameOrg.text, "too many runs in progress")

	otherOrg := make(chan probeOutcome, 1)

	go func() {
		otherOrg <- env.callProbe(t.Context(), env.claims(env.orgB, env.userB, scopeMCP), toolRunJSScript)
	}()

	waitStarted(t, runner)
	close(runner.release)

	r.Equal(http.StatusOK, <-restDone)

	outcome := <-otherOrg
	r.False(outcome.isError, outcome.text)
	r.Zero(limiter.BusyOrgs())
}
