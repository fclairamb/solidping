package checkjs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dop251/goja"

	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkrdp"
	checkrdpconfig "github.com/fclairamb/solidping/server/internal/checkers/checkrdp/config"
)

// maxRDPActions caps the session methods one execution may call — a separate
// budget from maxBrowserActions for the same reason browser actions got their
// own: an RDP session is a held slot plus many cheap round-trips, not 20 API
// calls.
const maxRDPActions = 100

// Errors the `rdp` global throws (infrastructure/script bugs — `error`
// status). Target verdicts come back as `{ ok: false }` values instead.
var (
	errRDPAlreadyOpen = errors.New(
		"an RDP session is already open: a script may open one RDP session per execution")
	errRDPTypeDisabled = errors.New(
		`check type "rdp" is disabled on this server`)
	errRDPSessionClosed = errors.New("no RDP session is open")
)

// rdpOptionKeys are the connect() option names. Width/height mirror the
// browser viewport: optional, defaulting to 1280x800.
type rdpConnectOptions struct {
	host     string
	username string
	password string
	domain   string
	width    int
	height   int
}

// registerRDP exposes the `rdp` global: `rdp.connect(options)` and nothing
// else — exactly the browser.open() shape. Every session action hangs off the
// object connect() returns, so the one-session rule lives in one place.
func (r *jsRuntime) registerRDP() {
	rdpObj := r.vm.NewObject()

	_ = rdpObj.Set("connect", func(call goja.FunctionCall) goja.Value {
		opts, _ := call.Argument(0).Export().(map[string]any)

		obj, fields, err := r.openRDPSession(opts)
		if err != nil {
			// Script bugs and infrastructure throw (error status); target
			// verdicts and slot timeouts were already returned as values.
			panic(r.vm.NewGoError(err))
		}

		if fields != nil && !fields[jsKeyOK].(bool) {
			// A target-side open failure (auth rejected, slot timeout):
			// returned as a value the script inspects and reports.
			return r.vm.ToValue(fields)
		}

		return obj
	})

	_ = r.vm.Set("rdp", rdpObj)
}

// openRDPSession is the body of rdp.connect: gate, budget, one-session rule,
// and the logon through checkrdp's seam. On failure it returns the
// `{ ok: false, error }` fields the script inspects; on success it returns
// the session object for the fields it already carries.
func (r *jsRuntime) openRDPSession(opts map[string]any) (*goja.Object, map[string]any, error) {
	// One session per execution, like one page.
	if r.rdpOpened {
		return nil, nil, errRDPAlreadyOpen
	}

	// The SERVER-level activation gate, the same one browser.open() consults.
	if TypeEnabled != nil && !TypeEnabled(checkerdef.CheckTypeRDP) {
		return nil, nil, errRDPTypeDisabled
	}

	parsed := parseRDPConnectOptions(opts)
	if parsedErr := validateRDPConnectOptions(parsed); parsedErr != nil {
		return nil, nil, parsedErr
	}

	// Counted BEFORE anything is dialed: a refused connect still spends its
	// unit (the same ordering every other budget uses).
	if r.rdpActions.Add(1) > int32(maxRDPActions) {
		return nil, rdpFailuref("rdp action limit of %d exceeded", maxRDPActions), nil
	}

	cfg := &checkrdpconfig.RDPConfig{
		Host:     parsed.host,
		Username: parsed.username,
		Password: parsed.password,
		Domain:   parsed.domain,
		// The JS session never reads the check's own timeout: the runtime
		// budget bounds every call through callContext.
	}

	session, err := checkrdp.OpenRDPSession(r.execCtx, cfg, parsed.width, parsed.height, nil)
	if err != nil {
		return nil, r.rdpOpenFailure(err), nil
	}

	r.rdp = session
	r.rdpOpened = true

	obj := r.newRDPObject(session)

	return obj, map[string]any{jsKeyOK: true}, nil
}

// parseRDPConnectOptions reads the option map.
func parseRDPConnectOptions(opts map[string]any) rdpConnectOptions {
	parsed := rdpConnectOptions{width: 1280, height: 800}

	if opts == nil {
		return parsed
	}

	parsed.host, _ = opts["host"].(string)
	parsed.username, _ = opts["username"].(string)
	parsed.password, _ = opts["password"].(string)
	parsed.domain, _ = opts["domain"].(string)

	if w, ok := numericOption(opts["width"]); ok && int(w) > 0 {
		parsed.width = int(w)
	}

	if h, ok := numericOption(opts["height"]); ok && int(h) > 0 {
		parsed.height = int(h)
	}

	return parsed
}

// validateRDPConnectOptions enforces what an authenticated logon requires.
func validateRDPConnectOptions(parsed rdpConnectOptions) error {
	if parsed.host == "" {
		return errors.New("rdp.connect: host is required")
	}

	if parsed.username == "" || parsed.password == "" {
		return errors.New("rdp.connect: username and password are required (an RDP session is a real logon)")
	}

	return nil
}

// rdpOpenFailure maps an open failure to the returned fields: target
// failures (auth rejected, server dropped) come back as values with their
// distinct code; slot timeouts come back as timeouts; anything else is
// infrastructure and PANICS (the runtime reports `error`), mirroring the
// browser's open contract.
func (r *jsRuntime) rdpOpenFailure(err error) map[string]any {
	fields := rdpFailure(err)
	if fields != nil {
		return fields
	}

	// Infrastructure: a failure to OPEN that is not a target verdict —
	// throw like browser.open() does.
	panic(r.vm.NewGoError(err))
}

// rdpFailure renders a target-side open failure as `{ ok: false }` fields,
// or nil when the failure is NOT a target verdict.
func rdpFailure(err error) map[string]any {
	var authErr *checkrdp.ErrAuthFailure

	if errors.As(err, &authErr) {
		return map[string]any{
			jsKeyOK:                   false,
			checkerdef.OutputKeyError: authErr.Error(),
			"failureCode":             authErr.Code(),
		}
	}

	if errors.Is(err, checkrdp.ErrSlotTimeout) {
		return map[string]any{
			jsKeyOK:                   false,
			checkerdef.OutputKeyError: err.Error(),
			jsKeyTimedOut:             true,
		}
	}

	return nil
}

// rdpSessionClosedFields is the "no session is open" failure, a constant
// message (err113: no dynamic errors).
func rdpSessionClosedFields() map[string]any {
	return map[string]any{
		jsKeyOK:                   false,
		checkerdef.OutputKeyError: errRDPSessionClosed.Error(),
	}
}

func rdpFailuref(format string, args ...any) map[string]any {
	return map[string]any{
		jsKeyOK:                   false,
		checkerdef.OutputKeyError: fmt.Sprintf(format, args...),
	}
}

// newRDPObject builds the goja object every rdp method hangs off.
//
//nolint:funlen // one binding per API method; splitting it hides the surface
func (r *jsRuntime) newRDPObject(session checkrdp.RDPSession) *goja.Object {
	obj := r.vm.NewObject()

	_ = obj.Set("waitForStable", func(call goja.FunctionCall) goja.Value {
		var opts map[string]any
		if len(call.Arguments) > 0 {
			opts, _ = call.Argument(0).Export().(map[string]any)
		}

		return r.rdpAction(func(ctx context.Context) (map[string]any, error) {
			quiet := stableQuietJS(opts)
			if q, ok := numericOption(opts["quietMs"]); ok && int64(q) > 0 {
				quiet = time.Duration(q) * time.Millisecond
			}

			start := time.Now()
			if waitErr := session.WaitForStable(ctx, quiet); waitErr != nil {
				return nil, waitErr
			}

			return map[string]any{jsKeyDuration: time.Since(start).Milliseconds()}, nil
		})
	})

	_ = obj.Set("waitForChange", func(call goja.FunctionCall) goja.Value {
		timeoutMs := 5000.0
		if v, ok := numericOption(call.Argument(0).Export()); ok && v > 0 {
			timeoutMs = v
		}

		return r.rdpAction(func(execCtx context.Context) (map[string]any, error) {
			timeout := time.Duration(timeoutMs) * time.Millisecond

			ctx, cancel, err := r.callContext(map[string]any{"timeout": timeout})
			if err != nil {
				return nil, err
			}

			defer cancel()

			if waitErr := session.WaitForChange(ctx, timeout); waitErr != nil {
				return nil, waitErr
			}

			return map[string]any{jsKeyOK: true}, nil
		})
	})

	_ = obj.Set("click", func(call goja.FunctionCall) goja.Value {
		x, y := intArgs(call)

		return r.rdpAction(func(_ context.Context) (map[string]any, error) {
			return nil, session.Click(x, y)
		})
	})

	_ = obj.Set("rightClick", func(call goja.FunctionCall) goja.Value {
		x, y := intArgs(call)

		return r.rdpAction(func(_ context.Context) (map[string]any, error) {
			return nil, session.RightClick(x, y)
		})
	})

	_ = obj.Set("doubleClick", func(call goja.FunctionCall) goja.Value {
		x, y := intArgs(call)

		return r.rdpAction(func(_ context.Context) (map[string]any, error) {
			return nil, session.DoubleClick(x, y)
		})
	})

	_ = obj.Set("move", func(call goja.FunctionCall) goja.Value {
		x, y := intArgs(call)

		return r.rdpAction(func(_ context.Context) (map[string]any, error) {
			return nil, session.Move(x, y)
		})
	})

	_ = obj.Set("type", func(call goja.FunctionCall) goja.Value {
		text := call.Argument(0).String()

		return r.rdpAction(func(_ context.Context) (map[string]any, error) {
			return nil, session.Type(text)
		})
	})

	_ = obj.Set("key", func(call goja.FunctionCall) goja.Value {
		key := call.Argument(0).String()

		return r.rdpAction(func(_ context.Context) (map[string]any, error) {
			return nil, session.Key(key)
		})
	})

	_ = obj.Set("pixel", func(call goja.FunctionCall) goja.Value {
		x, y := intArgs(call)

		return r.rdpAction(func(_ context.Context) (map[string]any, error) {
			color, err := session.Pixel(x, y)
			if err != nil {
				return nil, err
			}

			return map[string]any{
				"r": color.R, "g": color.G, "b": color.B,
			}, nil
		})
	})

	_ = obj.Set("regionHash", func(call goja.FunctionCall) goja.Value {
		x := numericOptionOrZero(call.Argument(0).Export())
		y := numericOptionOrZero(call.Argument(1).Export())
		w := numericOptionOrZero(call.Argument(2).Export())
		h := numericOptionOrZero(call.Argument(3).Export())

		return r.rdpAction(func(_ context.Context) (map[string]any, error) {
			hash, err := session.RegionHash(int(x), int(y), int(w), int(h))
			if err != nil {
				return nil, err
			}

			return map[string]any{"hash": hash}, nil
		})
	})

	_ = obj.Set("screenshot", func(_ goja.FunctionCall) goja.Value {
		return r.rdpAction(func(_ context.Context) (map[string]any, error) {
			shot, err := session.ScreenshotPNG()
			if err != nil {
				return nil, err
			}

			r.recordScreenshot(checkbrowser.Capture{Image: shot, Format: checkerdef.ImageFormatPNG})

			return map[string]any{jsKeyOK: true}, nil
		})
	})

	r.setUncountedRDPMethods(obj)

	return obj
}

// setUncountedRDPMethods attaches the methods that do NOT spend an action:
// logoff/disconnect/close — releasing a resource must never be the call that
// hits a limit (page.close's rule).
func (r *jsRuntime) setUncountedRDPMethods(obj *goja.Object) {
	_ = obj.Set("logoff", func(_ goja.FunctionCall) goja.Value {
		if r.rdp == nil {
			return r.vm.ToValue(rdpSessionClosedFields())
		}

		if err := r.rdp.EndLogoff(); err != nil {
			return r.vm.ToValue(rdpFailuref("logoff failed: %v", err))
		}

		return r.vm.ToValue(map[string]any{jsKeyOK: true})
	})

	_ = obj.Set("disconnect", func(_ goja.FunctionCall) goja.Value {
		if r.rdp == nil {
			return r.vm.ToValue(rdpSessionClosedFields())
		}

		r.rdp.EndDisconnect()

		return r.vm.ToValue(map[string]any{jsKeyOK: true})
	})

	// close() is an alias for logoff(), for parity with browser.
	_ = obj.Set("close", func(_ goja.FunctionCall) goja.Value {
		if r.rdp != nil {
			_ = r.rdp.EndLogoff()
		}

		return goja.Undefined()
	})
}

// rdpAction is the shared body of every counted rdp method: spend one unit of
// the RDP budget, run the action, split the outcome — infrastructure throws,
// target failures return `{ ok: false, error }` (pageAction's contract).
func (r *jsRuntime) rdpAction(run func(ctx context.Context) (map[string]any, error)) goja.Value {
	if r.rdp == nil {
		panic(r.vm.NewGoError(errRDPSessionClosed))
	}

	if r.rdpActions.Add(1) > int32(maxRDPActions) {
		return r.vm.ToValue(rdpFailuref("rdp action limit of %d exceeded", maxRDPActions))
	}

	fields, err := run(r.execCtx)
	if err != nil {
		if checkrdp.Infra(err) {
			panic(r.vm.NewGoError(err))
		}

		return r.vm.ToValue(map[string]any{
			jsKeyOK:                   false,
			checkerdef.OutputKeyError: err.Error(),
		})
	}

	if fields == nil {
		fields = map[string]any{}
	}

	fields[jsKeyOK] = true

	return r.vm.ToValue(fields)
}

// stableQuietJS is the default quiet window waitForStable uses when the
// script does not pass quietMs: 2 s, the same value the check itself uses.
func stableQuietJS(_ map[string]any) time.Duration {
	return 2 * time.Second
}

// closeRDP disposes the session if one was opened. Called from a defer in
// Execute, so a script that returns early, throws, or is interrupted never
// leaks an RDP session or its concurrency slot. A script that already called
// logoff()/disconnect() costs nothing here.
func (r *jsRuntime) closeRDP() {
	if r.rdp == nil {
		return
	}

	// A script that ended without calling either gets a log off — the check's
	// default end-session mode.
	if !r.rdp.Closed() {
		_ = r.rdp.EndLogoff()
	}
}

// intArgs reads the first two arguments of a call as ints (a point).
func intArgs(call goja.FunctionCall) (int, int) {
	return int(numericOptionOrZero(call.Argument(0).Export())),
		int(numericOptionOrZero(call.Argument(1).Export()))
}

// numericOptionOrZero reads a JS numeric argument, tolerating undefined.
func numericOptionOrZero(raw any) float64 {
	v, ok := numericOption(raw)
	if !ok {
		return 0
	}

	return v
}
