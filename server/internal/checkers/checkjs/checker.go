// Package checkjs provides custom JavaScript script execution checks.
package checkjs

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"

	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkjs/config"
	checkrdp "github.com/fclairamb/solidping/server/internal/checkers/checkrdp"
)

// JS result map keys.
const (
	jsKeyStatus   = "status"
	jsKeyOutput   = "output"
	jsKeyDuration = "duration"
)

// JS console log levels and status values.
const (
	logLevelLog   = "log"
	logLevelWarn  = "warn"
	logLevelError = "error"
	logLevelInfo  = "info"
)

// CheckerResolver is a function type that resolves a check type to a checker and checkconfig.
// It is set by the registry package during init to break the import cycle.
type CheckerResolver func(checkType checkerdef.CheckType) (checkerdef.Checker, checkerdef.Config, bool)

// ResolveChecker is the function used to resolve checkers for sub-checks.
// Must be set before executing JS checks that use solidping.check().
var ResolveChecker CheckerResolver //nolint:gochecknoglobals // Required to break import cycle

// TypeEnabledFunc reports whether a check type is enabled at the SERVER level
// (`checkers.enabled` / `checkers.disabled` / `checkers.enabled_labels`).
//
// It deliberately takes no org: the JS runtime has no org identity (see the
// gate in jsRuntime.check), so per-org overrides cannot be answered here and
// the signature says so rather than pretending otherwise.
type TypeEnabledFunc func(checkType checkerdef.CheckType) bool

// TypeEnabled is the server-level activation gate consulted before a sub-check
// runs. It mirrors the ResolveChecker indirection above — checkjs cannot import
// registry or config-derived resolvers without an import cycle — and is wired
// in checkworker.newCheckWorker, the one constructor both the embedded worker
// and the standalone agent funnel through.
//
// nil means "no gate configured": every type is allowed. That keeps this
// package usable standalone and in unit tests, and keeps the JS checker's
// behavior unchanged for any caller that never installs a gate.
var TypeEnabled TypeEnabledFunc //nolint:gochecknoglobals // Mirrors ResolveChecker: same import-cycle break

const (
	maxSubChecks     = 20
	maxConsoleOutput = 16 * 1024 // 16KB
	// maxHTTPBody is checkerdef's shared payload cap: the same number bounds a
	// page's text() / evaluate() result, so there is ONE number to change.
	maxHTTPBody = checkerdef.MaxPayloadBytes
	// maxRedirectsCap is both the default and the ceiling for a request's
	// `maxRedirects` option — the same 10 Go's own client defaults to.
	maxRedirectsCap = 10

	// redirectHostPolicyAny is the default `redirectHostPolicy` option value:
	// follow any hop regardless of host, today's behavior.
	redirectHostPolicyAny = "any"
	// redirectHostPolicySameHost refuses any redirect hop whose URL host
	// differs from the PREVIOUS hop's (spec 2026-09-25-21) — the checkjs twin
	// of checkhttp's HTTPConfig.RedirectHostPolicy, spelled as a per-request
	// option here because a script's HTTP calls have no check-level config of
	// their own to carry it.
	redirectHostPolicySameHost = "same-host"
)

// jsKeyStatusCode is the response/redirect field carrying an HTTP status.
const jsKeyStatusCode = "statusCode"

// Errors a script can provoke through the http helper's option map.
var (
	errInvalidTimeoutOption = errors.New(
		"invalid timeout: expected a duration string (\"2s\") or a number of milliseconds")
	errTooManyRedirects = errors.New("stopped after too many redirects")
	// errRedirectHostMismatch's wording matches checkhttp's
	// redirectHostMismatchError so the same phrase means the same thing
	// across both check types.
	errRedirectHostMismatch      = errors.New("redirect to different host refused")
	errInvalidRedirectHostPolicy = fmt.Errorf(
		"redirectHostPolicy must be %q or %q", redirectHostPolicyAny, redirectHostPolicySameHost)
)

// JSChecker implements the Checker interface for JavaScript checks.
type JSChecker struct{}

// Type returns the check type identifier.
func (c *JSChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeJS
}

// Validate checks if the configuration is valid. Every rule lives in the light
// `config` sub-package so an offline validator (`sp checks validate`) can run it
// without linking this checker's execution client.
func (c *JSChecker) Validate(spec *checkerdef.CheckSpec) error {
	return checkconfig.ValidateSpec(spec)
}

// Execute performs the JavaScript check and returns the result.
func (c *JSChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*JSConfig](config)
	if err != nil {
		return nil, err
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()

	runtime := newJSRuntime(ctx, cfg)

	// One page per execution, disposed here whatever the script did — returned
	// early, threw, or was interrupted. Nothing else releases the browser slot
	// (spec 2026-09-12-06 §3).
	defer runtime.closeBrowser()

	// Same rule for the RDP session: a script that returns early, throws or
	// is interrupted never leaks an authenticated logon or its slot — and a
	// script that ended without calling logoff() gets one, the check's
	// default end-session mode.
	defer runtime.closeRDP()

	// Same rule for every socket the script opened: a script that returns
	// early, throws or is interrupted never leaks a TCP/UDP/WebSocket
	// connection (spec 2026-09-15-06 §3).
	defer runtime.closeSockets()

	// Set up interrupt for timeout
	go func() {
		<-ctx.Done()
		runtime.vm.Interrupt("timeout")
	}()

	// contextcheck: the `browser` bindings derive their per-call contexts from
	// r.execCtx (the check's own budget), which is exactly the rule spec
	// 2026-09-12-06 §3 requires — there is no context parameter to thread
	// through a goja binding signature.
	//nolint:contextcheck // bindings derive from r.execCtx by design
	runtime.registerGlobals()

	// Wrap script in a function so top-level return statements work
	wrapped := "(function() {\n" + cfg.Script + "\n})()"

	val, err := runtime.vm.RunString(wrapped)
	runtime.vm.ClearInterrupt()

	duration := time.Since(start)

	result := runtime.resultFor(val, err, duration, timeout)

	// ONE exit point for the capture decision, and deliberately so: a
	// screenshot the script already took must survive every terminal status
	// the browser check would have kept one for — including the INTERRUPT
	// path, which is the case the feature exists for ("show me what the page
	// looked like when the login hung"). Attaching only on the normal-return
	// path silently threw that away. attachScreenshot itself decides what the
	// status earns (spec §5), so no branch here has to remember the rule.
	runtime.attachScreenshot(result)

	return result, nil
}

// resultFor turns the script's outcome — a returned value, a throw, or an
// interrupt — into the check's result. Split out of Execute so the capture
// decision above has a single result to reason about instead of four returns.
func (r *jsRuntime) resultFor(
	val goja.Value, err error, duration, timeout time.Duration,
) *checkerdef.Result {
	// The check's OWN deadline expiring is a `timeout`, not an `error`: the
	// runtime cut the script off, which is exactly what the status is for and
	// what the result contract already promises
	// (web/docs/docs/features/javascript-checks.md#result-contract). A
	// cancellation (worker shutdown) is not a timeout and stays `error`.
	//
	// Checked BEFORE the returned value, and that ordering is load-bearing
	// rather than tidy. goja's Interrupt only lands when control returns to
	// JavaScript AND the VM reaches a point that checks the flag: a script
	// whose last binding returns after the deadline can race past a couple of
	// statements and `return { status: "up" }` before the interrupt is
	// observed. Trusting the interrupt alone therefore makes the SAME run
	// report `timeout` or `up` depending on scheduling — the exact
	// nondeterminism spec 2026-09-12-06 §3 rules out ("the result is
	// timeout"). The budget is spent either way, so the budget decides.
	//
	// `executionDeadlineReached` rather than `execCtx.Err()` alone: a blocked
	// socket call panics the instant the CLOCK passes the deadline, which is a
	// hair before the context's timer fires. Classifying on `Err()` only meant
	// that panic could land here while `Err()` was still nil, turning the
	// budget expiring into `script error: GoError: context deadline exceeded`.
	if errors.Is(r.execCtx.Err(), context.DeadlineExceeded) ||
		r.executionDeadlineReached() {
		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: duration,
			Output:   r.buildOutput("script timed out after " + timeout.String()),
		}
	}

	if err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: duration,
			Output:   r.buildOutput("script error: " + err.Error()),
		}
	}

	if val == nil || goja.IsUndefined(val) || goja.IsNull(val) {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: duration,
			Output:   r.buildOutput("script must return a result object"),
		}
	}

	return r.parseResult(val, duration)
}

// jsRuntime holds the state for a single JS execution.
type jsRuntime struct {
	execCtx       context.Context //nolint:containedctx // Needed for sub-check and HTTP execution in JS callbacks
	vm            *goja.Runtime
	config        *JSConfig
	consoleBuf    bytes.Buffer
	subCheckCount atomic.Int32

	// browser is the page this execution opened, nil until browser.open().
	// browserOpened stays true after a close, which is what enforces the
	// one-page-per-execution rule against a script that closes and re-opens.
	browser       BrowserSession
	browserOpened bool
	// browserActions is the page-method budget, counted SEPARATELY from
	// subCheckCount — see maxBrowserActions.
	browserActions atomic.Int32

	// screenshot is the last successful page.screenshot() capture — bytes and
	// the format they are encoded in — kept only if the final verdict earns it
	// (see attachScreenshot).
	screenshot   checkbrowser.Capture
	screenshotAt time.Time

	// rdp is the RDP session this execution opened, nil until rdp.connect().
	// rdpOpened stays true after a close, which is what enforces the
	// one-session-per-execution rule against a script that closes and
	// re-connects.
	rdp       checkrdp.RDPSession
	rdpOpened bool
	// rdpActions is the session-action budget, counted SEPARATELY from
	// subCheckCount — see maxRDPActions.
	rdpActions atomic.Int32

	// sockets holds one disposer per connection the script opened through the
	// `tcp` / `udp` / `websocket` globals, in open order. Execute defers
	// closeSockets() over it, so nothing leaks whatever the script did.
	sockets []func()
	// socketCount is the per-execution CONNECTION budget (maxSocketConnections),
	// counted at open and never given back by a close: a closed connection is
	// one this script already spent.
	socketCount atomic.Int32
	// socketActions is the read/write budget (maxSocketActions), counted
	// SEPARATELY from subCheckCount for the same reason browserActions is —
	// see the comment on maxBrowserActions.
	socketActions atomic.Int32
	// payloadUsed is the shared byte pool socket reads draw from. See
	// remainingPayload.
	payloadUsed atomic.Int64
}

// remainingPayload is what is left of the execution's single maxHTTPBody-byte
// payload pool.
//
// Socket reads SPEND from this pool (a `read()` that accumulates 700 KiB leaves
// 324 KiB), and an HTTP response body is capped at whatever remains — so the
// 1 MiB in the Limits table is genuinely ONE number rather than one per
// transport. HTTP bodies deliberately do NOT spend: a script that never touches
// a socket keeps exactly the per-request 1 MiB cap it has always had.
func (r *jsRuntime) remainingPayload() int64 {
	remaining := int64(maxHTTPBody) - r.payloadUsed.Load()
	if remaining < 0 {
		return 0
	}

	return remaining
}

// spendPayload draws n bytes from the shared pool.
func (r *jsRuntime) spendPayload(n int) {
	if n > 0 {
		r.payloadUsed.Add(int64(n))
	}
}

// closeSockets disposes every connection the script opened, in open order.
// Each disposer is idempotent, so a script that closed its own handles costs
// nothing here.
func (r *jsRuntime) closeSockets() {
	for _, dispose := range r.sockets {
		dispose()
	}
}

// newJSRuntime creates a new jsRuntime with the given context and checkconfig.
func newJSRuntime(ctx context.Context, cfg *JSConfig) *jsRuntime {
	return &jsRuntime{
		execCtx: ctx,
		vm:      goja.New(),
		config:  cfg,
	}
}

// registerGlobals sets up the global objects available to the script.
func (r *jsRuntime) registerGlobals() {
	r.registerEnv()
	r.registerSecrets()
	r.registerConsole()
	r.registerSleep()
	r.registerSolidping()
	r.registerHTTP()
	r.registerBase64()
	r.registerBrowser()
	r.registerRDP()
	r.registerTCP()
	r.registerUDP()
	r.registerWebSocket()
}

// registerEnv exposes checkconfig.Env as a read-only "env" object.
func (r *jsRuntime) registerEnv() {
	envObj := r.vm.NewObject()

	for key, val := range r.config.Env {
		_ = envObj.Set(key, val)
	}

	_ = r.vm.Set("env", envObj)
}

// registerSecrets exposes checkconfig.Secrets as a read-only "secrets" object.
//
// Deliberately a mirror of registerEnv rather than a merge into it: the call
// site is meant to read what the value IS — `secrets.PASSWORD` next to
// `env.BASE_URL` — and the two maps are stored in different columns (public
// `config` vs the encrypted envelope). By the time Execute reaches here the
// effective config is already public ∪ decrypted-private, so both maps are
// simply present.
func (r *jsRuntime) registerSecrets() {
	secretsObj := r.vm.NewObject()

	for key, val := range r.config.Secrets {
		_ = secretsObj.Set(key, val)
	}

	_ = r.vm.Set("secrets", secretsObj)
}

// registerConsole exposes console.log/warn/error/info.
func (r *jsRuntime) registerConsole() {
	console := r.vm.NewObject()

	for _, level := range []string{logLevelLog, logLevelWarn, logLevelError, logLevelInfo} {
		lvl := level
		_ = console.Set(lvl, func(call goja.FunctionCall) goja.Value {
			r.writeConsole(lvl, call.Arguments)

			return goja.Undefined()
		})
	}

	_ = r.vm.Set("console", console)
}

// writeConsole writes a console line to the buffer, respecting the size cap.
func (r *jsRuntime) writeConsole(level string, args []goja.Value) {
	if r.consoleBuf.Len() >= maxConsoleOutput {
		return
	}

	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, arg.String())
	}

	line := fmt.Sprintf("[%s] %s\n", level, strings.Join(parts, " "))

	remaining := maxConsoleOutput - r.consoleBuf.Len()
	if len(line) > remaining {
		line = line[:remaining]
	}

	r.consoleBuf.WriteString(line)
}

// registerSleep exposes a sleep(ms) function.
func (r *jsRuntime) registerSleep() {
	_ = r.vm.Set("sleep", func(call goja.FunctionCall) goja.Value {
		millis := call.Argument(0).ToInteger()
		if millis <= 0 {
			return goja.Undefined()
		}

		select {
		case <-time.After(time.Duration(millis) * time.Millisecond):
		case <-r.execCtx.Done():
			panic(r.vm.NewGoError(r.execCtx.Err()))
		}

		return goja.Undefined()
	})
}

// registerBase64 exposes base64.encode(string)/decode(string), the one
// encoding/decoding primitive goja does not ship (btoa/atob are browser APIs,
// not ECMAScript, and the runtime registers neither).
//
// Standard (padded) encoding only — that is what HTTP Basic auth requires
// (`user:pass` -> base64 -> `Authorization: Basic <value>`) and what every
// other consumer of this global expects. Text only: goja strings are UTF-16,
// so a decode of arbitrary binary is not representable as a JS string — the
// documented contract is limited to text (Basic-auth credentials, JSON
// tokens), not general-purpose binary round-tripping.
func (r *jsRuntime) registerBase64() {
	base64Obj := r.vm.NewObject()

	_ = base64Obj.Set("encode", func(call goja.FunctionCall) goja.Value {
		input := call.Argument(0).String()

		return r.vm.ToValue(base64.StdEncoding.EncodeToString([]byte(input)))
	})

	_ = base64Obj.Set("decode", func(call goja.FunctionCall) goja.Value {
		input := call.Argument(0).String()

		decoded, err := base64.StdEncoding.DecodeString(input)
		if err != nil {
			// Malformed input MUST throw rather than return an empty or
			// partial string: a silent empty string would turn a typo'd
			// credential into a check that probes with an empty password and
			// reports "down" with nothing visibly wrong in the script itself.
			panic(r.vm.NewGoError(fmt.Errorf("base64.decode: %w", err)))
		}

		return r.vm.ToValue(string(decoded))
	})

	_ = r.vm.Set("base64", base64Obj)
}

// registerSolidping exposes the solidping.check() function and typed wrappers.
func (r *jsRuntime) registerSolidping() {
	solidping := r.vm.NewObject()

	_ = solidping.Set("check", func(call goja.FunctionCall) goja.Value {
		typeStr := call.Argument(0).String()

		configVal := call.Argument(1).Export()
		configMap, _ := configVal.(map[string]any)

		result := r.check(typeStr, configMap)

		return r.vm.ToValue(result)
	})

	// Typed wrappers for each supported check type
	// `browser` is here for the same reason every other type is: the generic
	// solidping.check("browser", …) form already worked (only `js` and
	// `heartbeat` are refused), so the wrapper's absence was an inconsistency
	// rather than a gate. Note this runs a WHOLE browser check and hands back
	// its verdict; to DRIVE a page, use the `browser` global instead.
	checkerTypes := []string{
		"http", "tcp", "dns", "ssl", "icmp", "smtp", "udp", "ssh",
		"pop3", "imap", "websocket", "postgresql", "ftp", "sftp", "domain",
		"browser", "rdp",
	}

	for _, typeName := range checkerTypes {
		checkType := typeName
		_ = solidping.Set(checkType, func(call goja.FunctionCall) goja.Value {
			configVal := call.Argument(0).Export()
			configMap, _ := configVal.(map[string]any)

			result := r.check(checkType, configMap)

			return r.vm.ToValue(result)
		})
	}

	_ = r.vm.Set("solidping", solidping)
}

// errSubCheckRDPLogon is the refusal an authenticated rdp sub-check gets.
const errSubCheckRDPLogon = "an authenticated RDP logon cannot run as a sub-check: its 15-minute " +
	"minimum period is enforced on the script's own check, which cannot see inside " +
	"sub-check arguments. Use rdp.connect(), which carries that floor"

// subCheckFloorRefusal refuses a sub-check whose CONFIG raises its own period
// floor (checkerdef.MinPeriodHint) — today, an rdp check with credentials.
//
// The floor is enforced at validation time on the outer `js` check, by a
// heuristic over its script text (rdp.connect). A sub-check's config is only
// built at run time, from arguments the validator never sees, so running it
// would let solidping.rdp({username, password}) — or solidping.check("rdp",
// …) — perform a real Windows logon at the js check's 30 s period. Refusing
// it at run time is the only way the floor holds whatever the call looks like.
// A pre-auth rdp sub-check (no credentials) has no hint and still runs.
func subCheckFloorRefusal(cfg checkerdef.Config) string {
	hinter, ok := cfg.(checkerdef.MinPeriodHint)
	if !ok || hinter.MinPeriodHint() == 0 {
		return ""
	}

	if sourcer, hasSource := cfg.(checkerdef.MinPeriodHintSource); hasSource &&
		sourcer.MinPeriodHintSource() == checkerdef.MinPeriodSourceRDP {
		return errSubCheckRDPLogon
	}

	return fmt.Sprintf("this sub-check's config requires a minimum period of %s, "+
		"which cannot be enforced on a sub-check", hinter.MinPeriodHint())
}

// jsCheckError builds the {status, output.error} shape every early return
// from check() below uses, so the function's body is refusals plus one line
// each rather than a repeated map literal.
func jsCheckError(msg string) map[string]any {
	return map[string]any{
		jsKeyStatus: logLevelError,
		jsKeyOutput: map[string]any{checkerdef.OutputKeyError: msg},
	}
}

// check executes a sub-checker via the resolver.
func (r *jsRuntime) check(typeStr string, configMap map[string]any) map[string]any {
	// Block recursive JS and heartbeat checks
	if typeStr == "js" || typeStr == "heartbeat" {
		return jsCheckError("check type \"" + typeStr + "\" is not allowed in JS scripts")
	}

	// Enforce sub-check limit
	if r.subCheckCount.Add(1) > int32(maxSubChecks) {
		return jsCheckError(fmt.Sprintf("sub-check limit of %d exceeded", maxSubChecks))
	}

	checkType := checkerdef.CheckType(typeStr)

	// Server-level activation gate. Without it, solidping.check() would run any
	// type the binary was compiled with, including ones the operator turned off
	// in checkers.enabled/disabled/enabled_labels — the dashboard would say
	// `browser` is disabled while a script still started a real Chrome.
	//
	// Two things this gate deliberately is NOT:
	//
	//   - It is not org-aware. The JS runtime has no org identity (Execute
	//     carries neither server config nor an org), so this answers the
	//     SERVER-level question only; a per-org `orgDisabled` entry is NOT
	//     honored here. Threading the org through Execute is a follow-up.
	//   - It is not folded into ResolveChecker's ok=false below, because that
	//     path means "unknown check type" and reporting a disabled type as
	//     nonexistent would be both false and unhelpful.
	//
	// Budget accounting: this runs AFTER the maxSubChecks counter above, so a
	// refused sub-check still consumes one unit of the budget. That is
	// deliberate — the existing guard ordering is left untouched and a script
	// cannot spin the refusal path for free.
	if TypeEnabled != nil && !TypeEnabled(checkType) {
		return jsCheckError("check type \"" + typeStr + "\" is disabled on this server")
	}

	// A tunneled script cannot let a sub-check of a type that itself lacks
	// SupportsTunnel probe from the WORKER's own network while the script
	// believes it is running behind the bastion — that silent local probing is
	// the security property this refusal exists to prevent. Types that do
	// declare SupportsTunnel need nothing here: they already read the dialer
	// off r.execCtx themselves. Uses the API's own sentence
	// (handlers/checks/tunnel.go) so the message is familiar wherever it
	// appears.
	if checkerdef.TunnelDialerFrom(r.execCtx) != nil {
		if meta := checkerdef.GetCheckTypeMeta(checkType); meta == nil || !meta.SupportsTunnel {
			return jsCheckError(fmt.Sprintf("check type %q cannot run through an SSH tunnel", typeStr))
		}
	}

	if ResolveChecker == nil {
		return jsCheckError("checker resolver not initialized")
	}

	checker, cfg, ok := ResolveChecker(checkType)
	if !ok {
		return jsCheckError("unknown check type: " + typeStr)
	}

	if err := cfg.FromMap(configMap); err != nil {
		return jsCheckError("invalid config: " + err.Error())
	}

	if refusal := subCheckFloorRefusal(cfg); refusal != "" {
		return jsCheckError(refusal)
	}

	result, err := checker.Execute(r.execCtx, cfg)
	if err != nil {
		return jsCheckError("execution error: " + err.Error())
	}

	return map[string]any{
		jsKeyStatus:   result.Status.String(),
		jsKeyDuration: result.Duration.Milliseconds(),
		"metrics":     result.Metrics,
		jsKeyOutput:   result.Output,
	}
}

// registerHTTP exposes http.get/post/put/patch/delete/head plus http.session().
//
// The bare http.* functions stay STATELESS (no cookie jar), exactly as they
// have always been: an existing script must not start carrying cookies between
// calls because this feature shipped. State is opt-in, through a session.
func (r *jsRuntime) registerHTTP() {
	httpObj := r.vm.NewObject()

	r.setRequestMethods(httpObj, nil)

	_ = httpObj.Set("session", func(_ goja.FunctionCall) goja.Value {
		session, err := r.newHTTPSession()
		if err != nil {
			panic(r.vm.NewGoError(err))
		}

		return session
	})

	_ = r.vm.Set("http", httpObj)
}

// setRequestMethods attaches the six verbs to obj, all bound to the same jar
// (nil for the stateless http.* object).
func (r *jsRuntime) setRequestMethods(obj *goja.Object, jar *boundedJar) {
	for _, methodName := range []string{"get", "post", "put", "patch", "delete", "head"} {
		method := strings.ToUpper(methodName)
		_ = obj.Set(methodName, func(call goja.FunctionCall) goja.Value {
			urlStr := call.Argument(0).String()

			var opts map[string]any
			if len(call.Arguments) > 1 {
				opts, _ = call.Argument(1).Export().(map[string]any)
			}

			result := r.httpRequest(jar, method, urlStr, opts)

			return r.vm.ToValue(result)
		})
	}
}

// newHTTPSession builds the object http.session() returns: the same six verbs,
// backed by one bounded cookie jar, plus cookies(url) so a script can assert on
// what the flow actually set.
func (r *jsRuntime) newHTTPSession() (*goja.Object, error) {
	jar, err := newBoundedJar()
	if err != nil {
		return nil, err
	}

	obj := r.vm.NewObject()

	r.setRequestMethods(obj, jar)

	_ = obj.Set("cookies", func(call goja.FunctionCall) goja.Value {
		return r.vm.ToValue(jar.snapshot(call.Argument(0).String()))
	})

	return obj, nil
}

// httpOptions is the parsed form of a request's option map.
type httpOptions struct {
	body            string
	hasBody         bool
	headers         map[string]string
	followRedirects bool
	maxRedirects    int
	// redirectHostPolicy is "" / redirectHostPolicyAny (follow anything, the
	// default) or redirectHostPolicySameHost (refuse a cross-host hop).
	redirectHostPolicy string
	timeout            time.Duration
}

// parseHTTPOptions reads the option map, applying the same defaults the http
// check type uses: redirects are followed, at most maxRedirectsCap of them, and
// the request may not outlive the check's own timeout.
func (r *jsRuntime) parseHTTPOptions(opts map[string]any) (httpOptions, error) {
	checkTimeout := r.config.Timeout
	if checkTimeout <= 0 {
		checkTimeout = defaultTimeout
	}

	parsed := httpOptions{
		followRedirects: true,
		maxRedirects:    maxRedirectsCap,
		timeout:         checkTimeout,
	}

	if opts == nil {
		return parsed, nil
	}

	if body, ok := opts["body"].(string); ok {
		parsed.body, parsed.hasBody = body, true
	}

	if headers, ok := opts["headers"].(map[string]any); ok {
		parsed.headers = make(map[string]string, len(headers))

		for name, val := range headers {
			if strVal, ok := val.(string); ok {
				parsed.headers[name] = strVal
			}
		}
	}

	if follow, ok := opts["followRedirects"].(bool); ok {
		parsed.followRedirects = follow
	}

	if maxRedirects, ok := numericOption(opts["maxRedirects"]); ok {
		parsed.maxRedirects = clampRedirects(int(maxRedirects))
	}

	// redirectHostPolicy (optional). An unknown value is rejected rather than
	// silently treated as "any" — same reasoning as checkhttp's ValidateSpec,
	// just checked at call time since a script's options are never validated
	// offline.
	if policy, ok := opts["redirectHostPolicy"]; ok {
		policyStr, ok := policy.(string)
		if !ok {
			return parsed, errInvalidRedirectHostPolicy
		}

		switch policyStr {
		case "", redirectHostPolicyAny, redirectHostPolicySameHost:
			parsed.redirectHostPolicy = policyStr
		default:
			return parsed, errInvalidRedirectHostPolicy
		}
	}

	timeout, err := optionTimeout(opts["timeout"])
	if err != nil {
		return parsed, err
	}

	// Clamped, never widened: the check's timeout is the budget the scheduler
	// allocated, and a script must not be able to hold a job open past it.
	if timeout > 0 && timeout < parsed.timeout {
		parsed.timeout = timeout
	}

	return parsed, nil
}

// clampRedirects keeps maxRedirects inside [0, maxRedirectsCap].
func clampRedirects(value int) int {
	if value < 0 {
		return 0
	}

	if value > maxRedirectsCap {
		return maxRedirectsCap
	}

	return value
}

// numericOption reads a JS number option, which goja exports as float64 or
// int64 depending on how it was written.
func numericOption(raw any) (float64, bool) {
	switch typed := raw.(type) {
	case float64:
		return typed, true
	case int64:
		return float64(typed), true
	case int:
		return float64(typed), true
	default:
		return 0, false
	}
}

// optionTimeout reads the `timeout` option: a duration string ("2s") or a
// number of milliseconds. Anything else is a script bug worth reporting rather
// than silently ignoring.
func optionTimeout(raw any) (time.Duration, error) {
	if raw == nil {
		return 0, nil
	}

	if str, ok := raw.(string); ok {
		parsed, err := time.ParseDuration(str)
		if err != nil {
			return 0, fmt.Errorf("invalid timeout %q: %w", str, err)
		}

		return parsed, nil
	}

	if millis, ok := numericOption(raw); ok {
		return time.Duration(millis) * time.Millisecond, nil
	}

	return 0, errInvalidTimeoutOption
}

// httpRequest performs an HTTP request and returns the result as a map. A nil
// jar means the stateless bare http.* behavior.
func (r *jsRuntime) httpRequest(
	jar *boundedJar, method, requestURL string, rawOpts map[string]any,
) map[string]any {
	// Count against sub-check limit
	if r.subCheckCount.Add(1) > int32(maxSubChecks) {
		return map[string]any{
			checkerdef.OutputKeyError: fmt.Sprintf("sub-check limit of %d exceeded", maxSubChecks),
		}
	}

	opts, err := r.parseHTTPOptions(rawOpts)
	if err != nil {
		return map[string]any{checkerdef.OutputKeyError: err.Error()}
	}

	var bodyReader io.Reader
	if opts.hasBody {
		bodyReader = strings.NewReader(opts.body)
	}

	req, err := http.NewRequestWithContext(r.execCtx, method, requestURL, bodyReader)
	if err != nil {
		return map[string]any{checkerdef.OutputKeyError: "failed to create request: " + err.Error()}
	}

	for headerName, headerVal := range opts.headers {
		req.Header.Set(headerName, headerVal)
	}

	// Recorded from inside CheckRedirect, which is called on the goroutine
	// running this request — the JS runtime is single-threaded and blocked in
	// client.Do while that happens, so a plain slice is safe here.
	redirects := make([]map[string]any, 0)

	client := &http.Client{
		Timeout:       opts.timeout,
		CheckRedirect: redirectPolicy(&opts, &redirects),
		// nil when untunneled and no IP version is pinned (js does not declare
		// SupportsIPVersion), which keeps http.DefaultTransport and its pooled
		// connections byte-for-byte as before this feature. When a dialer is
		// present the transport hands the raw host:port to it — no local
		// resolution — exactly like the http and prometheus checkers.
		//
		// Under an enforcing egress policy (spec 2026-09-25-19) the transport
		// dials through the guard instead: a script cannot reach a non-public
		// address, and the refusal comes back as the request's error.
		Transport: checkerdef.HTTPTransportFor(r.execCtx, false),
	}

	if jar != nil {
		client.Jar = jar
	}

	start := time.Now()

	resp, err := client.Do(req)
	if err != nil {
		return map[string]any{checkerdef.OutputKeyError: "request failed: " + err.Error()}
	}

	defer func() { _ = resp.Body.Close() }()

	duration := time.Since(start)

	// Read body capped at 1MB — minus whatever socket reads already drew from
	// the shared payload pool, so the documented 1 MiB is one number for the
	// whole execution rather than one per transport.
	body, err := io.ReadAll(io.LimitReader(resp.Body, r.remainingPayload()))
	if err != nil {
		return map[string]any{
			jsKeyStatusCode:           resp.StatusCode,
			checkerdef.OutputKeyError: "failed to read body: " + err.Error(),
			jsKeyDuration:             duration.Milliseconds(),
		}
	}

	result := map[string]any{
		jsKeyStatusCode: resp.StatusCode,
		"body":          string(body),
		"headers":       responseHeaders(resp),
		"url":           finalURL(resp, requestURL),
		"redirects":     redirects,
		jsKeyDuration:   duration.Milliseconds(),
	}

	// Present only when true, so an untunneled response's shape is unchanged.
	if checkerdef.TunnelDialerFrom(r.execCtx) != nil {
		result[jsKeyTunneled] = true
	}

	return result
}

// redirectPolicy builds the client's CheckRedirect: it records the chain and
// enforces followRedirects / maxRedirects.
//
// With following disabled it returns http.ErrUseLastResponse, which hands the
// 3xx itself back to the caller — Location intact — instead of an error. That
// is the OAuth case the spec is about: the 302 carrying `code=` IS the success
// signal, so it must be observable, and the chain stays empty because nothing
// was followed.
func redirectPolicy(opts *httpOptions, redirects *[]map[string]any) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if !opts.followRedirects {
			return http.ErrUseLastResponse
		}

		if len(via) >= opts.maxRedirects {
			return fmt.Errorf("%w: %d", errTooManyRedirects, opts.maxRedirects)
		}

		if req.Response != nil {
			*redirects = append(*redirects, map[string]any{
				jsKeyStatusCode: req.Response.StatusCode,
				"location":      req.Response.Header.Get("Location"),
			})
		}

		// redirectHostPolicy: same-host — refuse a hop whose host differs from
		// the PREVIOUS one. Checked here, before the request is ever built, so
		// a refused hop never reaches the transport (and therefore never
		// dials) at all.
		if opts.redirectHostPolicy == redirectHostPolicySameHost && len(via) > 0 {
			prevHost := via[len(via)-1].URL.Hostname()
			nextHost := req.URL.Hostname()

			if prevHost != nextHost {
				return fmt.Errorf("%w: %s -> %s", errRedirectHostMismatch, prevHost, nextHost)
			}
		}

		return nil
	}
}

// responseHeaders converts response headers to a JS-friendly map: canonical
// (Go-canonicalized) keys, a bare string for a single value and an array for a
// repeated one.
func responseHeaders(resp *http.Response) map[string]any {
	out := make(map[string]any, len(resp.Header))

	for headerName, headerValues := range resp.Header {
		if len(headerValues) == 1 {
			out[headerName] = headerValues[0]

			continue
		}

		out[headerName] = headerValues
	}

	return out
}

// finalURL reports the URL the response actually came from — the last hop of a
// followed redirect chain, or the requested URL when nothing moved.
func finalURL(resp *http.Response, requestURL string) string {
	if resp.Request != nil && resp.Request.URL != nil {
		return resp.Request.URL.String()
	}

	return requestURL
}

// parseResult extracts status, metrics, and output from the JS return value.
func (r *jsRuntime) parseResult(val goja.Value, duration time.Duration) *checkerdef.Result {
	obj := val.ToObject(r.vm)
	if obj == nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: duration,
			Output:   r.buildOutput("script must return an object"),
		}
	}

	status := r.parseStatus(obj)

	var metrics map[string]any
	if metricsVal := obj.Get("metrics"); metricsVal != nil && !goja.IsUndefined(metricsVal) {
		metrics, _ = metricsVal.Export().(map[string]any)
	}

	output := r.buildOutputFromObj(obj)

	return &checkerdef.Result{
		Status:   status,
		Duration: duration,
		Metrics:  metrics,
		Output:   output,
	}
}

// parseStatus extracts the status from the returned object.
func (r *jsRuntime) parseStatus(obj *goja.Object) checkerdef.Status {
	statusVal := obj.Get("status")
	if statusVal == nil || goja.IsUndefined(statusVal) {
		return checkerdef.StatusError
	}

	switch statusVal.String() {
	case "up":
		return checkerdef.StatusUp
	case "down":
		return checkerdef.StatusDown
	case "timeout":
		return checkerdef.StatusTimeout
	default:
		return checkerdef.StatusError
	}
}

// buildOutput creates an output map carrying an error message plus the console
// log. The key is always `error` — every caller reports a runtime-level
// failure, and a script's own output goes through buildOutputFromObj instead.
func (r *jsRuntime) buildOutput(value string) map[string]any {
	out := make(map[string]any)
	out[logLevelError] = value

	if r.consoleBuf.Len() > 0 {
		out["console"] = r.consoleBuf.String()
	}

	return out
}

// buildOutputFromObj merges the script's output field with console output.
func (r *jsRuntime) buildOutputFromObj(obj *goja.Object) map[string]any {
	out := make(map[string]any)

	if outputVal := obj.Get("output"); outputVal != nil && !goja.IsUndefined(outputVal) {
		if exported, ok := outputVal.Export().(map[string]any); ok {
			for key, val := range exported {
				out[key] = val
			}
		}
	}

	if r.consoleBuf.Len() > 0 {
		out["console"] = r.consoleBuf.String()
	}

	return out
}
