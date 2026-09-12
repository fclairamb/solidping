package checkjs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dop251/goja"

	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// maxBrowserActions caps the page methods one execution may call.
//
// It is a SEPARATE budget from maxSubChecks, deliberately (spec 2026-09-12-06
// §4): browser actions are a different resource — one held slot and many cheap
// CDP round-trips — and folding them into the 20-call `http.*`/`solidping.*`
// budget would let a single login flow exhaust the budget the script needs for
// the API call it was logging in *for*.
const maxBrowserActions = 100

// Page-result field names shared with the rest of the runtime.
const (
	// jsKeyOK is the field every page method sets: did the action succeed
	// against the target?
	jsKeyOK = "ok"
	// jsKeyValue is evaluate()'s payload field, and the cookie value field.
	jsKeyValue = "value"
	// jsKeyDomain is the cookie domain field.
	jsKeyDomain = "domain"
)

// Errors the `browser` global throws. All three are "the script or the worker
// is broken", which the runtime reports as status `error` — never `down`.
var (
	errPageAlreadyOpen = errors.New(
		"a page is already open: a script may open one browser page per execution")
	errBrowserTypeDisabled = errors.New(
		`check type "browser" is disabled on this server`)
)

// BrowserSession is the slice of checkbrowser.Session the `browser` bindings
// drive.
//
// It exists as an interface for exactly one reason: CI's backend job has no
// Chrome (only the E2E jobs install one), so every binding test runs against a
// fake through the OpenBrowser seam below. Production always gets the real
// *checkbrowser.Session.
//
// One method per documented page API call; a smaller interface would just be
// a worse mirror of what a script can do.
//
//nolint:interfacebloat // see above
type BrowserSession interface {
	Navigate(ctx context.Context, url string) (checkbrowser.NavResult, error)
	WaitVisible(ctx context.Context, sel string) error
	Click(ctx context.Context, sel string) error
	Fill(ctx context.Context, sel, text string) error
	Press(ctx context.Context, sel, key string) error
	Text(ctx context.Context, sel string) (string, error)
	Evaluate(ctx context.Context, expr string) (any, error)
	URL(ctx context.Context) (string, error)
	Cookies(ctx context.Context) ([]checkbrowser.Cookie, error)
	Screenshot(ctx context.Context) ([]byte, error)
	Close()
}

// OpenBrowser is the seam `browser.open()` goes through. Production points at
// checkbrowser.Open — the same slot, pre-flight, isolated context and eager
// allocation the `browser` check type uses. Tests replace it with a fake.
//
//nolint:gochecknoglobals // test seam, mirrors ResolveChecker / TypeEnabled
var OpenBrowser = func(ctx context.Context) (BrowserSession, error) {
	session, err := checkbrowser.Open(ctx)
	if err != nil {
		// Return a NIL interface, not a typed nil pointer: the caller checks
		// the error, but a later `if session != nil` elsewhere must not be
		// fooled by a non-nil interface wrapping a nil *Session.
		return nil, err
	}

	return session, nil
}

// registerBrowser exposes the `browser` global: `browser.open()` and nothing
// else. Everything a script does to the page hangs off the object open()
// returns, so there is one place that owns the one-page rule.
func (r *jsRuntime) registerBrowser() {
	browserObj := r.vm.NewObject()

	_ = browserObj.Set("open", func(_ goja.FunctionCall) goja.Value {
		page, err := r.openPage()
		if err != nil {
			// Every failure to OPEN is infrastructure or a script bug, never a
			// target verdict — so it throws, and the runtime reports `error`.
			panic(r.vm.NewGoError(err))
		}

		return page
	})

	_ = r.vm.Set("browser", browserObj)
}

// openPage acquires the session and builds the `page` object.
func (r *jsRuntime) openPage() (*goja.Object, error) {
	// One page per execution, for the whole execution: a closed page is not a
	// free slot to re-open, it is a page this script already spent.
	if r.browserOpened {
		return nil, errPageAlreadyOpen
	}

	// The SERVER-level activation gate, the same one sub-checks consult, with
	// the same message. An operator who turned `browser` off does not get it
	// back through a script. Like the sub-check gate it is not org-aware, for
	// the reason documented on jsRuntime.check — that follow-up covers both
	// paths and is not widened here.
	if TypeEnabled != nil && !TypeEnabled(checkerdef.CheckTypeBrowser) {
		return nil, errBrowserTypeDisabled
	}

	session, err := OpenBrowser(r.execCtx)
	if err != nil {
		return nil, err
	}

	r.browserOpened = true
	r.browser = session

	return r.newPageObject(), nil
}

// closeBrowser disposes the session if one was opened. Called from a defer in
// Execute, so a script that returns early, throws, or is interrupted never
// leaks a browser context or a concurrency slot. Session.Close is idempotent,
// so a script that called page.close() itself costs nothing here.
func (r *jsRuntime) closeBrowser() {
	if r.browser != nil {
		r.browser.Close()
	}
}

// newPageObject builds the goja object every page method hangs off.
//
//nolint:funlen // one binding per API method; splitting it hides the surface
func (r *jsRuntime) newPageObject() *goja.Object {
	page := r.vm.NewObject()

	_ = page.Set("goto", func(call goja.FunctionCall) goja.Value {
		url := call.Argument(0).String()

		return r.pageAction(func(session BrowserSession) (map[string]any, error) {
			// The same URL rules the browser check's own config enforces:
			// http/https only, file:/data:/javascript: refused.
			if err := checkbrowser.ValidateNavigationURL(url); err != nil {
				return nil, err
			}

			nav, err := session.Navigate(r.execCtx, url)
			if err != nil {
				return nil, err
			}

			return map[string]any{
				"url":         nav.URL,
				"title":       nav.Title,
				jsKeyDuration: nav.Duration.Milliseconds(),
			}, nil
		})
	})

	_ = page.Set("waitFor", func(call goja.FunctionCall) goja.Value {
		selector := call.Argument(0).String()

		var opts map[string]any
		if len(call.Arguments) > 1 {
			opts, _ = call.Argument(1).Export().(map[string]any)
		}

		return r.pageAction(func(session BrowserSession) (map[string]any, error) {
			ctx, cancel, err := r.pageCallContext(opts)
			if err != nil {
				return nil, err
			}

			defer cancel()

			start := time.Now()
			if waitErr := session.WaitVisible(ctx, selector); waitErr != nil {
				return nil, waitErr
			}

			return map[string]any{jsKeyDuration: time.Since(start).Milliseconds()}, nil
		})
	})

	_ = page.Set("click", func(call goja.FunctionCall) goja.Value {
		selector := call.Argument(0).String()

		return r.pageAction(func(session BrowserSession) (map[string]any, error) {
			return nil, session.Click(r.execCtx, selector)
		})
	})

	_ = page.Set("fill", func(call goja.FunctionCall) goja.Value {
		selector, text := call.Argument(0).String(), call.Argument(1).String()

		return r.pageAction(func(session BrowserSession) (map[string]any, error) {
			return nil, session.Fill(r.execCtx, selector, text)
		})
	})

	_ = page.Set("press", func(call goja.FunctionCall) goja.Value {
		selector, key := call.Argument(0).String(), call.Argument(1).String()

		return r.pageAction(func(session BrowserSession) (map[string]any, error) {
			return nil, session.Press(r.execCtx, selector, key)
		})
	})

	_ = page.Set("text", func(call goja.FunctionCall) goja.Value {
		selector := call.Argument(0).String()

		return r.pageAction(func(session BrowserSession) (map[string]any, error) {
			text, err := session.Text(r.execCtx, selector)
			if err != nil {
				return nil, err
			}

			return map[string]any{"text": text}, nil
		})
	})

	_ = page.Set("evaluate", func(call goja.FunctionCall) goja.Value {
		expression := call.Argument(0).String()

		return r.pageAction(func(session BrowserSession) (map[string]any, error) {
			value, err := session.Evaluate(r.execCtx, expression)
			if err != nil {
				return nil, err
			}

			return map[string]any{jsKeyValue: value}, nil
		})
	})

	_ = page.Set("screenshot", func(_ goja.FunctionCall) goja.Value {
		return r.pageAction(func(session BrowserSession) (map[string]any, error) {
			png, err := session.Screenshot(r.execCtx)
			if err != nil {
				return nil, err
			}

			r.recordScreenshot(png)

			// No payload beyond `ok`.
			return nil, nil
		})
	})

	r.setUncountedPageMethods(page)

	return page
}

// setUncountedPageMethods attaches the two methods that do NOT spend an action:
// url() (a single cheap read a script uses to assert where it landed) and
// close() (releasing a resource must never be the call that hits a limit).
func (r *jsRuntime) setUncountedPageMethods(page *goja.Object) {
	_ = page.Set("url", func(_ goja.FunctionCall) goja.Value {
		if r.browser == nil {
			return r.vm.ToValue("")
		}

		url, err := r.browser.URL(r.execCtx)
		if err != nil {
			if checkbrowser.Infra(err) {
				panic(r.vm.NewGoError(err))
			}

			return r.vm.ToValue("")
		}

		return r.vm.ToValue(url)
	})

	_ = page.Set("cookies", func(_ goja.FunctionCall) goja.Value {
		cookies := r.pageCookies()

		return r.vm.ToValue(cookies)
	})

	_ = page.Set("close", func(_ goja.FunctionCall) goja.Value {
		if r.browser != nil {
			r.browser.Close()
		}

		return goja.Undefined()
	})
}

// pageCookies is cookies() split out to keep the budget accounting visible:
// unlike url()/close() it IS a counted action, but its return shape is a plain
// array rather than the `{ ok }` envelope, so it cannot go through pageAction.
func (r *jsRuntime) pageCookies() []map[string]any {
	empty := []map[string]any{}

	if r.browser == nil {
		return empty
	}

	if r.browserActions.Add(1) > int32(maxBrowserActions) {
		return empty
	}

	cookies, err := r.browser.Cookies(r.execCtx)
	if err != nil {
		if checkbrowser.Infra(err) {
			panic(r.vm.NewGoError(err))
		}

		return empty
	}

	out := make([]map[string]any, 0, len(cookies))
	for i := range cookies {
		out = append(out, map[string]any{
			"name":      cookies[i].Name,
			jsKeyValue:  cookies[i].Value,
			jsKeyDomain: cookies[i].Domain,
			"path":      cookies[i].Path,
			"secure":    cookies[i].Secure,
			"httpOnly":  cookies[i].HTTPOnly,
			"expires":   cookies[i].Expires,
		})
	}

	return out
}

// pageAction is the shared body of every counted page method: spend one unit
// of the browser budget, run the action, and split its outcome the way the
// runtime's own contract splits outcomes.
//
//   - An INFRASTRUCTURE failure (the CDP socket died, the page is closed)
//     throws, so the check reports `error` — our worker is broken.
//   - Anything else — a selector that never appeared, a page that threw —
//     returns `{ ok: false, error }` and lets the SCRIPT decide the target is
//     down. Playwright throws on both; copying that would turn "the login
//     button never rendered" into an alert about SolidPing.
func (r *jsRuntime) pageAction(run func(session BrowserSession) (map[string]any, error)) goja.Value {
	if r.browser == nil {
		panic(r.vm.NewGoError(errPageClosed))
	}

	// Counted before the action runs, exactly like the sub-check budget: a
	// refused call still spends its unit, so a script cannot spin the refusal
	// path for free.
	if r.browserActions.Add(1) > int32(maxBrowserActions) {
		return r.vm.ToValue(map[string]any{
			jsKeyOK: false,
			checkerdef.OutputKeyError: fmt.Sprintf(
				"browser action limit of %d exceeded", maxBrowserActions),
		})
	}

	fields, err := run(r.browser)
	if err != nil {
		if checkbrowser.Infra(err) {
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

// errPageClosed is the impossible-by-construction case: a page method reached
// with no session behind it.
var errPageClosed = errors.New("no browser page is open")

// pageCallContext applies a per-call `timeout` option, with the same rule the
// `http.*` options use: a duration string or a number of milliseconds, clamped
// to the script's remaining time and never widening it. With no option the
// call simply runs on the execution context — which is what makes a `waitFor`
// on a selector that never appears end at the CHECK's timeout.
func (r *jsRuntime) pageCallContext(opts map[string]any) (context.Context, context.CancelFunc, error) {
	if opts == nil {
		return r.execCtx, func() {}, nil
	}

	timeout, err := optionTimeout(opts["timeout"])
	if err != nil {
		return nil, nil, err
	}

	if timeout <= 0 {
		return r.execCtx, func() {}, nil
	}

	ctx, cancel := context.WithTimeout(r.execCtx, timeout)

	return ctx, cancel, nil
}

// recordScreenshot keeps the LAST successful capture of the execution.
//
// Last wins so a script can overwrite an early "before" shot with the one it
// takes at the moment it decides the target is down. Whether the capture is
// KEPT is decided after the script returns — see Execute — so a shot on an
// `up` run costs a CDP round-trip and nothing else.
func (r *jsRuntime) recordScreenshot(png []byte) {
	if len(png) == 0 || len(png) > checkbrowser.MaxScreenshotBytes {
		return
	}

	r.screenshotPNG = png
	r.screenshotAt = time.Now()
}

// attachScreenshot hangs the recorded capture on a finished result, but only
// for the verdicts the browser check itself would have kept one for.
func (r *jsRuntime) attachScreenshot(result *checkerdef.Result) {
	if result == nil || r.screenshotPNG == nil {
		return
	}

	if !checkbrowser.CapturableStatus(result.Status) {
		return
	}

	if result.Diagnostics == nil {
		result.Diagnostics = &checkerdef.Diagnostics{}
	}

	result.Diagnostics.Screenshot = &checkerdef.Screenshot{
		PNG:        r.screenshotPNG,
		CapturedAt: r.screenshotAt,
	}
}
