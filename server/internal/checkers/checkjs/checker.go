// Package checkjs provides custom JavaScript script execution checks.
package checkjs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// CheckerResolver is a function type that resolves a check type to a checker and config.
// It is set by the registry package during init to break the import cycle.
type CheckerResolver func(checkType checkerdef.CheckType) (checkerdef.Checker, checkerdef.Config, bool)

// ResolveChecker is the function used to resolve checkers for sub-checks.
// Must be set before executing JS checks that use solidping.check().
var ResolveChecker CheckerResolver //nolint:gochecknoglobals // Required to break import cycle

const (
	maxSubChecks     = 20
	maxConsoleOutput = 16 * 1024   // 16KB
	maxHTTPBody      = 1024 * 1024 // 1MB
)

// JSChecker implements the Checker interface for JavaScript checks.
type JSChecker struct{}

// Type returns the check type identifier.
func (c *JSChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeJS
}

// Validate checks if the configuration is valid.
func (c *JSChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &JSConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Name == "" {
		spec.Name = "JS Script"
	}

	if spec.Slug == "" {
		spec.Slug = "js-script"
	}

	return nil
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

	// Set up interrupt for timeout
	go func() {
		<-ctx.Done()
		runtime.vm.Interrupt("timeout")
	}()

	runtime.registerGlobals()

	// Wrap script in a function so top-level return statements work
	wrapped := "(function() {\n" + cfg.Script + "\n})()"

	val, err := runtime.vm.RunString(wrapped)
	runtime.vm.ClearInterrupt()

	duration := time.Since(start)

	if err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: duration,
			Output:   runtime.buildOutput("error", "script error: "+err.Error()),
		}, nil
	}

	if val == nil || goja.IsUndefined(val) || goja.IsNull(val) {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: duration,
			Output:   runtime.buildOutput("error", "script must return a result object"),
		}, nil
	}

	return runtime.parseResult(val, duration), nil
}

// jsRuntime holds the state for a single JS execution.
type jsRuntime struct {
	execCtx       context.Context //nolint:containedctx // Needed for sub-check and HTTP execution in JS callbacks
	vm            *goja.Runtime
	config        *JSConfig
	consoleBuf    bytes.Buffer
	subCheckCount atomic.Int32
}

// newJSRuntime creates a new jsRuntime with the given context and config.
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
	r.registerConsole()
	r.registerSleep()
	r.registerSolidping()
	r.registerHTTP()
}

// registerEnv exposes config.Env as a read-only "env" object.
func (r *jsRuntime) registerEnv() {
	envObj := r.vm.NewObject()

	for key, val := range r.config.Env {
		_ = envObj.Set(key, val)
	}

	_ = r.vm.Set("env", envObj)
}

// registerConsole exposes console.log/warn/error/info.
func (r *jsRuntime) registerConsole() {
	console := r.vm.NewObject()

	for _, level := range []string{"log", "warn", "error", "info"} {
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
	checkerTypes := []string{
		"http", "tcp", "dns", "ssl", "icmp", "smtp", "udp", "ssh",
		"pop3", "imap", "websocket", "postgresql", "ftp", "sftp", "domain",
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

// check executes a sub-checker via the resolver.
func (r *jsRuntime) check(typeStr string, configMap map[string]any) map[string]any {
	// Block recursive JS and heartbeat checks
	if typeStr == "js" || typeStr == "heartbeat" {
		return map[string]any{
			"status": "error",
			"output": map[string]any{
				"error": "check type \"" + typeStr + "\" is not allowed in JS scripts",
			},
		}
	}

	// Enforce sub-check limit
	if r.subCheckCount.Add(1) > int32(maxSubChecks) {
		return map[string]any{
			"status": "error",
			"output": map[string]any{
				"error": fmt.Sprintf("sub-check limit of %d exceeded", maxSubChecks),
			},
		}
	}

	if ResolveChecker == nil {
		return map[string]any{
			"status": "error",
			"output": map[string]any{"error": "checker resolver not initialized"},
		}
	}

	checkType := checkerdef.CheckType(typeStr)

	checker, cfg, ok := ResolveChecker(checkType)
	if !ok {
		return map[string]any{
			"status": "error",
			"output": map[string]any{"error": "unknown check type: " + typeStr},
		}
	}

	if err := cfg.FromMap(configMap); err != nil {
		return map[string]any{
			"status": "error",
			"output": map[string]any{"error": "invalid config: " + err.Error()},
		}
	}

	result, err := checker.Execute(r.execCtx, cfg)
	if err != nil {
		return map[string]any{
			"status": "error",
			"output": map[string]any{"error": "execution error: " + err.Error()},
		}
	}

	return map[string]any{
		"status":   result.Status.String(),
		"duration": result.Duration.Milliseconds(),
		"metrics":  result.Metrics,
		"output":   result.Output,
	}
}

// registerHTTP exposes http.get/post/put/patch/delete/head functions.
func (r *jsRuntime) registerHTTP() {
	httpObj := r.vm.NewObject()

	for _, methodName := range []string{"get", "post", "put", "patch", "delete", "head"} {
		method := strings.ToUpper(methodName)
		_ = httpObj.Set(methodName, func(call goja.FunctionCall) goja.Value {
			urlStr := call.Argument(0).String()

			var opts map[string]any
			if len(call.Arguments) > 1 {
				opts, _ = call.Argument(1).Export().(map[string]any)
			}

			result := r.httpRequest(method, urlStr, opts)

			return r.vm.ToValue(result)
		})
	}

	_ = r.vm.Set("http", httpObj)
}

// httpRequest performs an HTTP request and returns the result as a map.
func (r *jsRuntime) httpRequest(method, requestURL string, opts map[string]any) map[string]any {
	// Count against sub-check limit
	if r.subCheckCount.Add(1) > int32(maxSubChecks) {
		return map[string]any{
			"error": fmt.Sprintf("sub-check limit of %d exceeded", maxSubChecks),
		}
	}

	var bodyReader io.Reader
	if opts != nil {
		if body, ok := opts["body"].(string); ok {
			bodyReader = strings.NewReader(body)
		}
	}

	req, err := http.NewRequestWithContext(r.execCtx, method, requestURL, bodyReader)
	if err != nil {
		return map[string]any{"error": "failed to create request: " + err.Error()}
	}

	// Set headers from opts
	if opts != nil {
		if headers, ok := opts["headers"].(map[string]any); ok {
			for headerName, headerVal := range headers {
				if strVal, ok := headerVal.(string); ok {
					req.Header.Set(headerName, strVal)
				}
			}
		}
	}

	start := time.Now()

	client := &http.Client{}

	resp, err := client.Do(req)
	if err != nil {
		return map[string]any{"error": "request failed: " + err.Error()}
	}

	defer func() { _ = resp.Body.Close() }()

	duration := time.Since(start)

	// Read body capped at 1MB
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxHTTPBody)))
	if err != nil {
		return map[string]any{
			"statusCode": resp.StatusCode,
			"error":      "failed to read body: " + err.Error(),
			"duration":   duration.Milliseconds(),
		}
	}

	// Convert response headers to map
	respHeaders := make(map[string]any, len(resp.Header))
	for headerName, headerValues := range resp.Header {
		if len(headerValues) == 1 {
			respHeaders[headerName] = headerValues[0]
		} else {
			respHeaders[headerName] = headerValues
		}
	}

	return map[string]any{
		"statusCode": resp.StatusCode,
		"body":       string(body),
		"headers":    respHeaders,
		"duration":   duration.Milliseconds(),
	}
}

// parseResult extracts status, metrics, and output from the JS return value.
func (r *jsRuntime) parseResult(val goja.Value, duration time.Duration) *checkerdef.Result {
	obj := val.ToObject(r.vm)
	if obj == nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: duration,
			Output:   r.buildOutput("error", "script must return an object"),
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

// buildOutput creates an output map with the console log and an optional error.
func (r *jsRuntime) buildOutput(key, value string) map[string]any {
	out := make(map[string]any)
	out[key] = value

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
