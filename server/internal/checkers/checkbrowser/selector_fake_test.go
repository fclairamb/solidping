package checkbrowser

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// resolverScript answers the page resolver's evaluation: every prefixed
// selector resolves through resolverJS, so the fake decides how many elements
// "the page" has for it.
func resolverScript(matches int, errText string) func(string) (any, string) {
	return func(expr string) (any, string) {
		if !strings.Contains(expr, ".apply(null,") {
			return nil, ""
		}

		if matches == 1 {
			return map[string]any{"n": 1, "sel": `window.__spT["x"]`}, ""
		}

		return map[string]any{"n": matches, "err": errText}, ""
	}
}

//nolint:paralleltest // mutates the process-wide settings
func TestResolveSelectorAgainstScriptedPage(t *testing.T) {
	r := require.New(t)
	fake := newFakeBrowser(t)
	session, ctx := openFakeSession(t, fake)

	// A plain CSS selector is never tagged: nothing is asked of the page.
	tgt, release, err := session.resolveSelector(ctx, "button.go")
	r.NoError(err)
	r.Equal("button.go", tgt.sel)
	release()

	// One visible match: a JS-path target, and release cleans the tag up.
	fake.onEvaluate(resolverScript(1, ""))

	tgt, release, err = session.resolveSelector(ctx, "text=Sign in")
	r.NoError(err)
	r.Equal(`window.__spT["x"]`, tgt.sel)
	release()

	// Several matches for a single selector name the problem.
	fake.onEvaluate(resolverScript(3, "3 elements match"))

	_, _, err = session.resolveSelector(ctx, "text=Sign in")
	r.ErrorIs(err, errSelector)
	r.ErrorContains(err, "3 elements match")

	// A list reports every entry that failed.
	_, _, err = session.resolveSelector(ctx, EncodeSelectorList([]string{"text=A", "label=B"}))
	r.ErrorIs(err, errSelector)
	r.ErrorContains(err, "no entry resolves to exactly one visible element")
	r.ErrorContains(err, "(3 matches)")

	// A failing evaluation is not a selector error.
	fake.onEvaluate(func(string) (any, string) { return nil, "boom" })

	_, _, err = session.resolveSelector(ctx, "xpath///button")
	r.Error(err)
	r.NotErrorIs(err, errSelector)
}

//nolint:paralleltest // mutates the process-wide settings
func TestResolveAriaSelectorQueriesTheAccessibilityTree(t *testing.T) {
	r := require.New(t)
	fake := newFakeBrowser(t)

	var query map[string]any

	fake.on("Accessibility.queryAXTree", func(params json.RawMessage) (any, *fakeCDPError) {
		r.NoError(json.Unmarshal(params, &query))

		return map[string]any{"nodes": []any{
			map[string]any{"nodeId": "1", "ignored": false, "backendDOMNodeId": 0},
			map[string]any{"nodeId": "2", "ignored": false, "backendDOMNodeId": 7},
			map[string]any{"nodeId": "3", "ignored": false, "backendDOMNodeId": 8},
		}}, nil
	})
	fake.on("DOM.resolveNode", func(params json.RawMessage) (any, *fakeCDPError) {
		if strings.Contains(string(params), `"backendNodeId":8`) {
			return nil, &fakeCDPError{Code: -32000, Message: "No node with given id found"}
		}

		return map[string]any{"object": map[string]any{"type": "object", "objectId": "n7"}}, nil
	})
	fake.onEvaluate(resolverScript(1, ""))

	session, ctx := openFakeSession(t, fake)

	_, release, err := session.resolveSelector(ctx, `aria/Sign in[role="button"]`)
	r.NoError(err)
	release()

	r.Equal("Sign in", query["accessibleName"])
	r.Equal("button", query["role"])
	r.Equal(1, fake.called("Runtime.callFunctionOn"), "only the resolvable element is tagged")

	// A failing accessibility query is reported, not swallowed.
	fake.on("Accessibility.queryAXTree", func(json.RawMessage) (any, *fakeCDPError) {
		return nil, &fakeCDPError{Code: -32000, Message: "no ax tree"}
	})

	_, _, err = session.resolveSelector(ctx, "aria/Anything")
	r.ErrorContains(err, "querying the accessibility tree")
}

func TestAriaRoleAndShownSelector(t *testing.T) {
	t.Parallel()

	name, role := ariaRole(`Go[role="button"]`)
	require.Equal(t, "Go", name)
	require.Equal(t, "button", role)

	name, role = ariaRole("Plain")
	require.Equal(t, "Plain", name)
	require.Empty(t, role)

	require.Equal(t, `aria/Go`, shownSelector("aria", "Go"))
	require.Equal(t, `text="Go"`, shownSelector("text", "Go"))
}

//nolint:paralleltest // mutates the process-wide settings
func TestWaitResolvedPollsUntilTheElementAppears(t *testing.T) {
	r := require.New(t)
	fake := newFakeBrowser(t)
	session, ctx := openFakeSession(t, fake)

	polls := 0

	fake.onEvaluate(func(expr string) (any, string) {
		if !strings.Contains(expr, ".apply(null,") {
			return nil, ""
		}

		polls++
		if polls < 3 {
			return map[string]any{"n": 0, "err": "not yet"}, ""
		}

		return map[string]any{"n": 1, "sel": `window.__spT["x"]`}, ""
	})

	r.NoError(session.WaitVisible(ctx, "text=Later"))
	r.GreaterOrEqual(polls, 3)

	// Never appearing ends with an error once ctx is over.
	fake.onEvaluate(resolverScript(0, "nothing there"))

	shortCtx, cancel := contextWithTimeout(t, 400*time.Millisecond)
	defer cancel()

	err := session.WaitVisible(shortCtx, "text=Never")
	r.Error(err)

	// A plain CSS selector goes through chromedp's own wait.
	shortCtx2, cancel2 := contextWithTimeout(t, 400*time.Millisecond)
	defer cancel2()

	r.Error(session.WaitVisible(shortCtx2, "#missing"))
}

//nolint:paralleltest // mutates the process-wide settings
func TestSelectSubmitAndCheckVerdicts(t *testing.T) {
	r := require.New(t)
	fake := newFakeBrowser(t)
	session, ctx := openFakeSession(t, fake)

	verdict := ""

	fake.onEvaluate(func(expr string) (any, string) {
		switch {
		case strings.Contains(expr, ".apply(null,"):
			return map[string]any{"n": 1, "sel": `window.__spT["x"]`}, ""
		case strings.Contains(expr, "requestSubmit"), strings.Contains(expr, "no option matches"):
			return verdict, ""
		}

		return nil, ""
	})

	r.NoError(session.Select(ctx, "text=Country", "France"))
	r.NoError(session.Submit(ctx, "text=Form"))

	verdict = "not in a form"

	err := session.Submit(ctx, "text=Form")
	r.ErrorIs(err, errSelector)
	r.ErrorContains(err, "not in a form")

	err = session.Select(ctx, "text=Country", "Atlantis")
	r.ErrorIs(err, errSelector)
}

// TestElementActionsSurfaceAMissingNode: the fake page has no node behind the
// resolved path, so each action must report the failure instead of passing.
//
//nolint:paralleltest // mutates the process-wide settings
func TestElementActionsSurfaceAMissingNode(t *testing.T) {
	actions := map[string]func(*Session, context.Context) error{
		"click": func(s *Session, ctx context.Context) error { return s.Click(ctx, "text=Form") },
		"fill":  func(s *Session, ctx context.Context) error { return s.Fill(ctx, "text=Form", "x") },
		"press": func(s *Session, ctx context.Context) error { return s.Press(ctx, "text=Form", "Enter") },
		"text": func(s *Session, ctx context.Context) error {
			_, err := s.Text(ctx, "text=Form")

			return err
		},
	}

	for name, action := range actions {
		t.Run(name, func(t *testing.T) {
			fake := newFakeBrowser(t)
			fake.onEvaluate(resolverScript(1, ""))

			session, ctx := openFakeSession(t, fake)

			// chromedp waits for the node to appear: bound that wait.
			actionCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()

			require.Error(t, action(session, actionCtx))
		})
	}
}

//nolint:paralleltest // mutates the process-wide settings
func TestSelectorPageFailuresAreReported(t *testing.T) {
	r := require.New(t)

	// The document cannot be read: the aria query reports it.
	fake := newFakeBrowser(t)
	session, ctx := openFakeSession(t, fake)

	fake.on("Runtime.evaluate", func(params json.RawMessage) (any, *fakeCDPError) {
		if strings.Contains(string(params), `"expression":"document"`) {
			return nil, &fakeCDPError{Code: -32000, Message: "no document"}
		}

		return map[string]any{"result": map[string]any{"type": "undefined"}}, nil
	})

	_, _, err := session.resolveSelector(ctx, "aria/Go")
	r.ErrorContains(err, "reading the document")

	// Back to the default answers.
	fake.on("Runtime.evaluate", nil)

	// An action whose selector resolves to nothing never touches the page.
	fake.onEvaluate(resolverScript(0, "no such element"))

	err = session.Click(ctx, "text=Missing")
	r.ErrorIs(err, errSelector)
	r.ErrorContains(err, "no such element")

	// A page that throws while a prefixed wait polls it is not retried forever.
	fake.onEvaluate(func(string) (any, string) { return nil, "page crashed" })
	r.Error(session.WaitVisible(ctx, "text=Go"))

	// The select script throwing is a page error, not a verdict.
	fake.onEvaluate(func(expr string) (any, string) {
		if strings.Contains(expr, ".apply(null,") {
			return map[string]any{"n": 1, "sel": `window.__spT["x"]`}, ""
		}

		return nil, "select script threw"
	})

	err = session.Select(ctx, "text=Go", "x")
	r.Error(err)
	r.NotErrorIs(err, errSelector)
}

//nolint:paralleltest // mutates the process-wide settings
func TestResolveAriaSkipsElementsThatCannotBeTagged(t *testing.T) {
	r := require.New(t)
	fake := newFakeBrowser(t)

	fake.on("Accessibility.queryAXTree", func(json.RawMessage) (any, *fakeCDPError) {
		return map[string]any{"nodes": []any{
			map[string]any{"nodeId": "2", "ignored": false, "backendDOMNodeId": 7},
		}}, nil
	})
	fake.on("Runtime.callFunctionOn", func(json.RawMessage) (any, *fakeCDPError) {
		return nil, &fakeCDPError{Code: -32000, Message: "gone"}
	})
	fake.onEvaluate(resolverScript(0, "no match"))

	session, ctx := openFakeSession(t, fake)

	_, _, err := session.resolveSelector(ctx, "aria/Go")
	r.ErrorIs(err, errSelector)
	r.Equal(1, fake.called("Runtime.callFunctionOn"))
}

func TestEnsureChecked(t *testing.T) {
	t.Parallel()

	boolPtr := func(b bool) *bool { return &b }
	noClick := func() error { return nil }

	reads := func(states ...*bool) func() (*bool, error) {
		idx := 0

		return func() (*bool, error) {
			state := states[min(idx, len(states)-1)]
			idx++

			return state, nil
		}
	}

	require.NoError(t, ensureChecked("cb", true, reads(boolPtr(true)), noClick))
	require.NoError(t, ensureChecked("cb", true, reads(boolPtr(false), boolPtr(true)), noClick))

	err := ensureChecked("cb", true, reads(nil), noClick)
	require.ErrorIs(t, err, errSelector)
	require.ErrorContains(t, err, "is gone")

	err = ensureChecked("cb", false, reads(boolPtr(true), boolPtr(true)), noClick)
	require.ErrorContains(t, err, "is not unchecked after the click")

	err = ensureChecked("cb", true, reads(boolPtr(false), nil), noClick)
	require.ErrorContains(t, err, "is not checked after the click")

	boom := errRendererGone
	require.ErrorIs(t, ensureChecked("cb", true, func() (*bool, error) { return nil, boom }, noClick), boom)
	require.ErrorIs(t, ensureChecked("cb", true, reads(boolPtr(false)), func() error { return boom }), boom)

	calls := 0
	readTwice := func() (*bool, error) {
		calls++
		if calls == 2 {
			return nil, boom
		}

		return boolPtr(false), nil
	}
	require.ErrorIs(t, ensureChecked("cb", true, readTwice, noClick), boom)
}

//nolint:paralleltest // mutates the process-wide settings
func TestCheckAndUncheckThroughThePage(t *testing.T) {
	r := require.New(t)
	fake := newFakeBrowser(t)
	session, ctx := openFakeSession(t, fake)

	fake.onEvaluate(func(expr string) (any, string) {
		switch {
		case strings.Contains(expr, ".apply(null,"):
			return map[string]any{"n": 1, "sel": `window.__spT["x"]`}, ""
		case strings.Contains(expr, "e.checked"):
			return true, ""
		}

		return nil, ""
	})

	r.NoError(session.Check(ctx, "text=Remember"))

	// Already checked, and a click cannot be performed on the fake page:
	// Uncheck must fail on the click, not pretend it worked.
	clickCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	r.Error(session.Uncheck(clickCtx, "text=Remember"))
}
