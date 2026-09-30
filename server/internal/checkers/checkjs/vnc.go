package checkjs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dop251/goja"

	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkvnc"
)

// maxVNCActions caps the session methods one execution may call, a budget
// separate from the browser and RDP ones for the same reason.
const maxVNCActions = 100

// vncOptTimeout is the per-call timeout option name.
const vncOptTimeout = "timeout"

// Errors the `vnc` global throws (script bugs and infrastructure: `error`
// status). Target verdicts come back as `{ ok: false }` values instead.
var (
	errVNCAlreadyOpen = errors.New(
		"a VNC session is already open: a script may open one VNC session per execution")
	errVNCTypeDisabled = errors.New(
		`check type "vnc" is disabled on this server`)
	errVNCSessionClosed = errors.New("no VNC session is open")
	errVNCHostRequired  = errors.New("vnc.connect: host is required")
)

// vncConnectOptions are the connect() option names.
type vncConnectOptions struct {
	host     string
	port     int
	password string
	timeout  time.Duration
}

// registerVNC exposes the `vnc` global: `vnc.connect(options)` and nothing
// else, exactly the rdp.connect() shape.
func (r *jsRuntime) registerVNC() {
	vncObj := r.vm.NewObject()

	_ = vncObj.Set("connect", func(call goja.FunctionCall) goja.Value {
		opts, _ := call.Argument(0).Export().(map[string]any)

		obj, fields, err := r.openVNCSession(opts)
		if err != nil {
			panic(r.vm.NewGoError(err))
		}

		if okField, isBool := fields[jsKeyOK].(bool); !isBool || !okField {
			// A target-side open failure: returned as a value.
			return r.vm.ToValue(fields)
		}

		return obj
	})

	_ = r.vm.Set("vnc", vncObj)
}

// openVNCSession is the body of vnc.connect: gate, validation, budget,
// one-session rule, then the connection through checkvnc's seam.
func (r *jsRuntime) openVNCSession(opts map[string]any) (*goja.Object, map[string]any, error) {
	if r.vncOpened {
		return nil, nil, errVNCAlreadyOpen
	}

	if TypeEnabled != nil && !TypeEnabled(checkerdef.CheckTypeVNC) {
		return nil, nil, errVNCTypeDisabled
	}

	parsed := parseVNCConnectOptions(opts)
	if parsed.host == "" {
		return nil, nil, errVNCHostRequired
	}

	// Counted BEFORE anything is dialed: a refused connect still spends its unit.
	if r.vncActions.Add(1) > int32(maxVNCActions) {
		return nil, vncFailuref("vnc action limit of %d exceeded", maxVNCActions), nil
	}

	requireAuth := false // a script may drive a passwordless desktop

	cfg := &checkvnc.VNCConfig{
		Host: parsed.host, Port: parsed.port, Password: parsed.password,
		Timeout: parsed.timeout, RequireAuth: &requireAuth,
	}

	session, err := checkvnc.OpenVNCSession(r.execCtx, cfg, nil)
	if err != nil {
		fields := vncOpenFailure(r.execCtx, err)
		if fields == nil {
			return nil, nil, err
		}

		return nil, fields, nil
	}

	r.vnc = session
	r.vncOpened = true

	return r.newVNCObject(session), map[string]any{jsKeyOK: true}, nil
}

func parseVNCConnectOptions(opts map[string]any) vncConnectOptions {
	var parsed vncConnectOptions
	if opts == nil {
		return parsed
	}

	parsed.host, _ = opts["host"].(string)
	parsed.password, _ = opts["password"].(string)

	if p, ok := numericOption(opts["port"]); ok && int(p) > 0 {
		parsed.port = int(p)
	}

	if t, ok := numericOption(opts[vncOptTimeout]); ok && t > 0 {
		parsed.timeout = time.Duration(t) * time.Millisecond
	}

	return parsed
}

// vncOpenFailure renders a target-side open failure as `{ ok: false }` fields,
// or nil when the failure is not a target verdict (the caller throws).
func vncOpenFailure(ctx context.Context, err error) map[string]any {
	var failed *checkvnc.FailureError

	if errors.As(err, &failed) {
		fields := map[string]any{
			jsKeyOK:                   false,
			checkerdef.OutputKeyError: failed.Error(),
			"failureCode":             string(failed.Code),
		}

		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			fields[jsKeyTimedOut] = true
		}

		return fields
	}

	return nil
}

func vncFailuref(format string, args ...any) map[string]any {
	return map[string]any{
		jsKeyOK:                   false,
		checkerdef.OutputKeyError: fmt.Sprintf(format, args...),
	}
}

// newVNCObject builds the goja object every vnc method hangs off.
//
//nolint:funlen // one binding per API method; splitting it hides the surface
func (r *jsRuntime) newVNCObject(session checkvnc.VNCSession) *goja.Object {
	obj := r.vm.NewObject()

	_ = obj.Set("waitForStable", func(call goja.FunctionCall) goja.Value {
		var opts map[string]any
		if len(call.Arguments) > 0 {
			opts, _ = call.Argument(0).Export().(map[string]any)
		}

		return r.vncAction(func(ctx context.Context) (map[string]any, error) {
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

		return r.vncAction(func(_ context.Context) (map[string]any, error) {
			waitCtx, cancel, err := r.callContext(map[string]any{vncOptTimeout: timeoutMs})
			if err != nil {
				return nil, err
			}

			defer cancel()

			//nolint:contextcheck // the child context IS derived from execCtx
			if waitErr := session.WaitForChange(waitCtx, time.Duration(timeoutMs)*time.Millisecond); waitErr != nil {
				return nil, waitErr
			}

			return map[string]any{jsKeyOK: true}, nil
		})
	})

	pointer := func(name string, do func(x, y int) error) {
		_ = obj.Set(name, func(call goja.FunctionCall) goja.Value {
			x, y := intArgs(call)

			return r.vncAction(func(_ context.Context) (map[string]any, error) {
				return nil, do(x, y)
			})
		})
	}

	pointer("click", session.Click)
	pointer("rightClick", session.RightClick)
	pointer("doubleClick", session.DoubleClick)
	pointer("move", session.Move)

	_ = obj.Set("type", func(call goja.FunctionCall) goja.Value {
		text := call.Argument(0).String()

		return r.vncAction(func(_ context.Context) (map[string]any, error) {
			return nil, session.Type(text)
		})
	})

	_ = obj.Set("key", func(call goja.FunctionCall) goja.Value {
		key := call.Argument(0).String()

		return r.vncAction(func(_ context.Context) (map[string]any, error) {
			return nil, session.Key(key)
		})
	})

	_ = obj.Set("pixel", func(call goja.FunctionCall) goja.Value {
		x, y := intArgs(call)

		return r.vncAction(func(_ context.Context) (map[string]any, error) {
			color, err := session.Pixel(x, y)
			if err != nil {
				return nil, err
			}

			return map[string]any{"r": color.R, "g": color.G, "b": color.B}, nil
		})
	})

	_ = obj.Set("regionHash", func(call goja.FunctionCall) goja.Value {
		hashX := numericOptionOrZero(call.Argument(0).Export())
		hashY := numericOptionOrZero(call.Argument(1).Export())
		hashWidth := numericOptionOrZero(call.Argument(2).Export())
		hashHeight := numericOptionOrZero(call.Argument(3).Export())

		return r.vncAction(func(_ context.Context) (map[string]any, error) {
			hash, err := session.RegionHash(int(hashX), int(hashY), int(hashWidth), int(hashHeight))
			if err != nil {
				return nil, err
			}

			return map[string]any{"hash": hash}, nil
		})
	})

	_ = obj.Set("screenshot", func(_ goja.FunctionCall) goja.Value {
		return r.vncAction(func(_ context.Context) (map[string]any, error) {
			shot, err := session.ScreenshotPNG()
			if err != nil {
				r.screenshotErr = err.Error()

				return nil, err
			}

			r.recordScreenshot(checkbrowser.Capture{Image: shot, Format: checkerdef.ImageFormatPNG})

			return map[string]any{jsKeyOK: true}, nil
		})
	})

	r.setUncountedVNCMethods(obj)

	return obj
}

// setUncountedVNCMethods attaches disconnect()/close(): releasing a resource
// must never be the call that hits a limit.
func (r *jsRuntime) setUncountedVNCMethods(obj *goja.Object) {
	_ = obj.Set("disconnect", func(_ goja.FunctionCall) goja.Value {
		if r.vnc == nil {
			return r.vm.ToValue(map[string]any{
				jsKeyOK: false, checkerdef.OutputKeyError: errVNCSessionClosed.Error(),
			})
		}

		r.vnc.End()

		return r.vm.ToValue(map[string]any{jsKeyOK: true})
	})

	_ = obj.Set("close", func(_ goja.FunctionCall) goja.Value {
		if r.vnc != nil {
			r.vnc.End()
		}

		return goja.Undefined()
	})
}

// vncAction is the shared body of every counted vnc method: spend one unit,
// run the action, split the outcome (infrastructure throws, target failures
// return `{ ok: false, error }`).
func (r *jsRuntime) vncAction(run func(ctx context.Context) (map[string]any, error)) goja.Value {
	if r.vnc == nil {
		panic(r.vm.NewGoError(errVNCSessionClosed))
	}

	if r.vncActions.Add(1) > int32(maxVNCActions) {
		return r.vm.ToValue(vncFailuref("vnc action limit of %d exceeded", maxVNCActions))
	}

	fields, err := run(r.execCtx)
	if err != nil {
		if checkvnc.Infra(err) {
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

// closeVNC disposes the session if one was opened. Deferred from Execute so a
// script that returns early, throws or is interrupted never leaks a connection.
func (r *jsRuntime) closeVNC() {
	if r.vnc != nil {
		r.vnc.End()
	}
}
