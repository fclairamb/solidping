package checkbrowser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/dom"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Selector kinds a script can prefix its selector with, Playwright-style.
const (
	kindCSS   = "css"
	kindText  = "text"
	kindLabel = "label"
	kindName  = "name"
	kindAuto  = "auto"

	// Chrome DevTools Recorder syntax, kept verbatim by imports.
	kindAria   = "aria"
	kindXPath  = "xpath"
	kindPierce = "pierce"
)

// waitPollInterval is how often waitFor re-runs the resolver of a non-CSS
// selector.
const waitPollInterval = 100 * time.Millisecond

// errSelector is the base of every "could not resolve this selector" error.
var errSelector = errors.New("selector")

// parseSelector splits a prefixed selector into its kind and value. An
// unprefixed selector, or `css=`, is plain CSS — exactly as before prefixes
// existed, so "button" keeps meaning the CSS tag.
func parseSelector(sel string) (string, string) {
	for _, kind := range []string{kindCSS, kindText, kindLabel, kindName, kindAuto} {
		if rest, ok := strings.CutPrefix(sel, kind+"="); ok {
			return kind, rest
		}
	}

	// Recorder syntax: `kind/value`. text/ is text=.
	if rest, ok := strings.CutPrefix(sel, "text/"); ok {
		return kindText, rest
	}

	for _, kind := range []string{kindAria, kindXPath, kindPierce} {
		if rest, ok := strings.CutPrefix(sel, kind+"/"); ok {
			return kind, rest
		}
	}

	return kindCSS, sel
}

// parseSelectorList decodes a selector argument: either one selector or, when
// the script passed an array, a JSON array of fallbacks (the checkjs binding
// encodes it so the Session API keeps a plain string). The second result tells
// whether it was a list. A CSS selector is never a valid JSON string array.
func parseSelectorList(sel string) ([]string, bool) {
	if strings.HasPrefix(sel, "[") {
		var list []string
		if err := json.Unmarshal([]byte(sel), &list); err == nil && len(list) > 0 {
			return list, true
		}
	}

	return []string{sel}, false
}

// EncodeSelectorList encodes fallback selectors into the single string the
// Session methods take.
func EncodeSelectorList(list []string) string {
	encoded, _ := json.Marshal(list) //nolint:errchkjson // strings always marshal

	return string(encoded)
}

// needsResolver reports whether sel cannot be handed to chromedp as CSS.
func needsResolver(sel string) bool {
	list, isList := parseSelectorList(sel)
	if isList {
		return true
	}

	kind, _ := parseSelector(list[0])

	return kind != kindCSS
}

// target is a resolved element, addressed the way chromedp queries it.
type target struct {
	sel  string
	opt  chromedp.QueryOption
	elem string // JS expression evaluating to the element
}

func cssTarget(css string) target {
	return target{sel: css, opt: chromedp.ByQuery, elem: "document.querySelector(" + jsString(css) + ")"}
}

// resolverJS finds the element a non-CSS selector designates, keeps only the
// VISIBLE ones, requires exactly one, tags it with a per-call attribute and
// returns {sel} (the CSS selector of the tag) or {err}. Evaluated with
// (kind, value, nonce) appended as the call arguments.
const resolverJS = `(function(kind, value, nonce, shown) {
  var norm = function(s) { return (s || "").replace(/\s+/g, " ").trim().toLowerCase(); };
  var visible = function(e) {
    if (!e || !e.getClientRects || e.getClientRects().length === 0) return false;
    var st = getComputedStyle(e);
    return st.visibility !== "hidden" && st.display !== "none";
  };
  var uniq = function(list) {
    var out = [];
    list.forEach(function(e) { if (e && out.indexOf(e) < 0) out.push(e); });
    return out;
  };
  var vis = function(list) { return uniq(list).filter(visible); };
  var all = function(q) { return Array.prototype.slice.call(document.querySelectorAll(q)); };
  var match = function(have, want, exact) {
    have = norm(have);
    return exact ? have === want : have.indexOf(want) >= 0;
  };
  var byCSS = function() {
    try { return vis(all(value)); } catch (e) { return []; }
  };
  var byName = function() {
    return vis(all('[name="' + value.replace(/\\/g, "\\\\").replace(/"/g, '\\"') + '"]'));
  };
  var byText = function() {
    var want = norm(value);
    var clickable = all('button, a, [role="button"], input[type="submit"], input[type="button"]');
    var any = all("body *");
    for (var pass = 0; pass < 2; pass++) {
      var exact = pass === 0;
      var found = vis(clickable.filter(function(e) {
        var t = e.tagName === "INPUT" ? e.value : e.textContent;
        return match(t, want, exact);
      }));
      if (found.length) return found;
      found = vis(any.filter(function(e) {
        var own = "";
        for (var i = 0; i < e.childNodes.length; i++) {
          if (e.childNodes[i].nodeType === 3) own += e.childNodes[i].nodeValue;
        }
        return own.trim() !== "" && match(own, want, exact);
      }));
      if (found.length) return found;
    }
    return [];
  };
  var byLabel = function() {
    var want = norm(value);
    for (var pass = 0; pass < 2; pass++) {
      var exact = pass === 0;
      var controls = [];
      all("label").forEach(function(l) {
        if (match(l.textContent, want, exact)) controls.push(l.control);
      });
      all("[aria-label]").forEach(function(e) {
        if (match(e.getAttribute("aria-label"), want, exact)) controls.push(e);
      });
      all("[aria-labelledby]").forEach(function(e) {
        var text = e.getAttribute("aria-labelledby").split(/\s+/).map(function(id) {
          var ref = document.getElementById(id);
          return ref ? ref.textContent : "";
        }).join(" ");
        if (match(text, want, exact)) controls.push(e);
      });
      var found = vis(controls);
      if (found.length) return found;
      found = vis(all("[placeholder]").filter(function(e) {
        return match(e.getAttribute("placeholder"), want, exact);
      }));
      if (found.length) return found;
    }
    return [];
  };
  var deep = function(q, root) {
    var out = Array.prototype.slice.call(root.querySelectorAll(q));
    root.querySelectorAll("*").forEach(function(e) {
      if (e.shadowRoot) out = out.concat(deep(q, e.shadowRoot));
    });
    return out;
  };
  var byPierce = function() {
    try { return vis(deep(value, document)); } catch (e) { return []; }
  };
  var byXPath = function() {
    var out = [];
    try {
      var r = document.evaluate(value, document, null, XPathResult.ORDERED_NODE_SNAPSHOT_TYPE, null);
      for (var i = 0; i < r.snapshotLength; i++) {
        if (r.snapshotItem(i).nodeType === 1) out.push(r.snapshotItem(i));
      }
    } catch (e) { return []; }
    return vis(out);
  };
  // Elements the Go side already picked through the accessibility tree.
  var byCand = function() {
    var q = all('[data-sp-cand="' + value + '"]');
    q.forEach(function(e) { e.removeAttribute("data-sp-cand"); });
    return vis(q);
  };
  var finders = {css: byCSS, name: byName, label: byLabel, text: byText,
    pierce: byPierce, xpath: byXPath, cand: byCand};
  var found = [];
  if (kind === "auto") {
    var order = ["css", "name", "label", "text"];
    for (var i = 0; i < order.length && !found.length; i++) found = finders[order[i]]();
  } else {
    found = finders[kind]();
  }
  if (found.length === 0) return {n: 0, err: "no element matches " + shown};
  if (found.length > 1) {
    return {n: found.length, err: shown + " matches " + found.length + " elements, make it more specific"};
  }
  found[0].setAttribute("data-sp-target", nonce);
  // Held on window too: an element inside a shadow root is unreachable by a
  // document-level CSS selector.
  (window.__spT = window.__spT || {})[nonce] = found[0];
  return {n: 1, sel: "window.__spT[" + JSON.stringify(nonce) + "]"};
})`

// resolved is the outcome of resolving one selector entry.
type resolved struct {
	t       target
	release func()
	n       int    // visible matches
	errMsg  string // set unless n == 1
}

// resolveSelector turns a selector, or a list of fallbacks, into a target by
// tagging the matching element. The returned release removes the tag (best
// effort); for a plain CSS selector nothing is tagged and release is a no-op.
func (s *Session) resolveSelector(ctx context.Context, sel string) (target, func(), error) {
	entries, isList := parseSelectorList(sel)

	if !isList {
		if kind, value := parseSelector(entries[0]); kind == kindCSS {
			return cssTarget(value), func() {}, nil
		}
	}

	var failures []string

	for _, entry := range entries {
		res, err := s.resolveEntry(ctx, entry)
		if err != nil {
			return target{}, nil, err
		}

		if res.n == 1 {
			return res.t, res.release, nil
		}

		if !isList {
			return target{}, nil, fmt.Errorf("%w: %s", errSelector, res.errMsg)
		}

		failures = append(failures, fmt.Sprintf("%s (%d matches)", entry, res.n))
	}

	return target{}, nil, fmt.Errorf("%w: no entry resolves to exactly one visible element: %s",
		errSelector, strings.Join(failures, "; "))
}

func shownSelector(kind, value string) string {
	switch kind {
	case kindAria, kindXPath, kindPierce:
		return kind + "/" + value
	default:
		return kind + "=" + jsString(value)
	}
}

// ariaRole splits `Name[role="button"]` into the name and the role.
func ariaRole(value string) (string, string) {
	const suffix = `"]`

	if idx := strings.LastIndex(value, `[role="`); idx >= 0 && strings.HasSuffix(value, suffix) {
		return value[:idx], value[idx+len(`[role="`) : len(value)-len(suffix)]
	}

	return value, ""
}

// tagAria marks the elements the accessibility tree says have the accessible
// name (and role) with data-sp-cand=nonce, computed by Chrome rather than
// re-implemented in JS.
func (s *Session) tagAria(ctx context.Context, value, nonce string) error {
	name, role := ariaRole(value)

	return s.run(ctx, chromedp.ActionFunc(func(cdpCtx context.Context) error {
		// The document is reached through Runtime, not dom.GetDocument: that call
		// replaces the DOM tree chromedp tracks and wedges its later actions.
		doc, _, err := cdpruntime.Evaluate("document").Do(cdpCtx)
		if err != nil {
			return fmt.Errorf("reading the document: %w", err)
		}

		query := accessibility.QueryAXTree().WithObjectID(doc.ObjectID)
		if name != "" {
			query = query.WithAccessibleName(name)
		}

		if role != "" {
			query = query.WithRole(role)
		}

		nodes, err := query.Do(cdpCtx)
		if err != nil {
			return fmt.Errorf("querying the accessibility tree: %w", err)
		}

		arg, _ := json.Marshal(nonce)

		for _, node := range nodes {
			if node.BackendDOMNodeID == 0 {
				continue
			}

			obj, resolveErr := dom.ResolveNode().WithBackendNodeID(node.BackendDOMNodeID).Do(cdpCtx)
			if resolveErr != nil {
				continue
			}

			// Text nodes (a button's StaticText child) are skipped: only elements.
			_, _, callErr := cdpruntime.CallFunctionOn(
				`function(n){ if (this.nodeType === 1) this.setAttribute("data-sp-cand", n); }`).
				WithObjectID(obj.ObjectID).
				WithArguments([]*cdpruntime.CallArgument{{Value: arg}}).
				Do(cdpCtx)
			if callErr != nil {
				continue
			}
		}

		return nil
	}))
}

// resolveEntry resolves one selector (not a list) through the page resolver.
func (s *Session) resolveEntry(ctx context.Context, entry string) (resolved, error) {
	kind, value := parseSelector(entry)

	nonce, err := newNonce()
	if err != nil {
		return resolved{}, err
	}

	shown := shownSelector(kind, value)
	jsKind, jsValue := kind, value

	if kind == kindAria {
		if tagErr := s.tagAria(ctx, value, nonce); tagErr != nil {
			return resolved{}, tagErr
		}

		jsKind, jsValue = "cand", nonce
	}

	args, err := json.Marshal([]string{jsKind, jsValue, nonce, shown})
	if err != nil {
		return resolved{}, fmt.Errorf("encoding selector: %w", err)
	}

	// The args slice is spread into the call: JSON array -> arguments.
	expr := resolverJS + ".apply(null, " + string(args) + ")"

	var res struct {
		N   int    `json:"n"`
		Sel string `json:"sel"`
		Err string `json:"err"`
	}

	if runErr := s.run(ctx, chromedp.Evaluate(expr, &res)); runErr != nil {
		return resolved{}, runErr
	}

	if res.N != 1 {
		return resolved{n: res.N, errMsg: res.Err}, nil
	}

	release := func() {
		cleanup := `document.querySelectorAll('[data-sp-target="` + nonce + `"]')` +
			`.forEach(function(e){e.removeAttribute("data-sp-target")});` +
			`if (window.__spT) delete window.__spT[` + jsString(nonce) + `]`

		var ignored any

		_ = s.run(ctx, chromedp.Evaluate(cleanup, &ignored))
	}

	return resolved{
		t:       target{sel: res.Sel, opt: chromedp.ByJSPath, elem: res.Sel},
		release: release,
		n:       1,
	}, nil
}

// withTarget resolves sel and runs action on the resulting element.
func (s *Session) withTarget(ctx context.Context, sel string, action func(tgt target) error) error {
	tgt, release, err := s.resolveSelector(ctx, sel)
	if err != nil {
		return err
	}

	defer release()

	return action(tgt)
}

// waitResolved polls the resolver of a non-CSS selector until it yields
// exactly one visible element or ctx ends, in which case the last resolver
// error is returned.
func (s *Session) waitResolved(ctx context.Context, sel string) error {
	for {
		_, release, err := s.resolveSelector(ctx, sel)
		if err == nil {
			release()

			return nil
		}

		if !errors.Is(err, errSelector) {
			return err
		}

		select {
		case <-ctx.Done():
			return err
		case <-time.After(waitPollInterval):
		}
	}
}

func newNonce() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating selector nonce: %w", err)
	}

	return hex.EncodeToString(buf), nil
}

func jsString(value string) string {
	// Encoding a string cannot fail.
	encoded, _ := json.Marshal(value) //nolint:errchkjson // a string always marshals

	return string(encoded)
}

// Check puts a checkbox/radio in the checked state, clicking only when it
// differs, then asserts the state.
func (s *Session) Check(ctx context.Context, sel string) error {
	return s.setChecked(ctx, sel, true)
}

// Uncheck is Check's opposite.
func (s *Session) Uncheck(ctx context.Context, sel string) error {
	return s.setChecked(ctx, sel, false)
}

func (s *Session) setChecked(ctx context.Context, sel string, want bool) error {
	return s.withTarget(ctx, sel, func(tgt target) error {
		read := `(function(){var e=` + tgt.elem + `;` +
			`return e ? !!e.checked : null})()`

		var state *bool
		if err := s.run(ctx, chromedp.Evaluate(read, &state)); err != nil {
			return err
		}

		if state == nil {
			return fmt.Errorf("%w: %q is gone", errSelector, sel)
		}

		if *state != want {
			if err := s.run(ctx, chromedp.Click(tgt.sel, tgt.opt)); err != nil {
				return err
			}

			if err := s.run(ctx, chromedp.Evaluate(read, &state)); err != nil {
				return err
			}
		}

		if state == nil || *state != want {
			return fmt.Errorf("%w: %q is not %s after the click", errSelector, sel, checkedWord(want))
		}

		return nil
	})
}

func checkedWord(want bool) string {
	if want {
		return "checked"
	}

	return "unchecked"
}

// selectJS picks an option by value, then by visible text, and dispatches
// input + change like a user would.
const selectJS = `(function(e, want) {
  if (!e || e.tagName !== "SELECT") return "not a <select>";
  var opts = Array.prototype.slice.call(e.options);
  var norm = function(s) { return (s || "").replace(/\s+/g, " ").trim(); };
  var opt = opts.filter(function(o) { return o.value === want; })[0] ||
    opts.filter(function(o) { return norm(o.text) === norm(want); })[0];
  if (!opt) return "no option matches " + JSON.stringify(want);
  // The native setter, so a React-controlled <select> sees the change.
  e.value = opt.value;
  opt.selected = true;
  e.dispatchEvent(new Event("input", {bubbles: true}));
  e.dispatchEvent(new Event("change", {bubbles: true}));
  return "";
})`

// Select chooses an option of a <select>, by value then by visible text.
func (s *Session) Select(ctx context.Context, sel, option string) error {
	return s.withTarget(ctx, sel, func(tgt target) error {
		return s.evalVerdict(ctx, selectJS+"("+tgt.elem+","+jsString(option)+")", sel)
	})
}

// submitJS submits the form (or the form owning the element) through
// requestSubmit, so validation and submit handlers run.
const submitJS = `(function(e) {
  if (!e) return "element is gone";
  var f = e.tagName === "FORM" ? e : (e.form || e.closest("form"));
  if (!f) return "not in a form";
  f.requestSubmit();
  return "";
})`

// Submit submits the form designated by sel, or owning the element sel does.
func (s *Session) Submit(ctx context.Context, sel string) error {
	return s.withTarget(ctx, sel, func(tgt target) error {
		return s.evalVerdict(ctx, submitJS+"("+tgt.elem+")", sel)
	})
}

// evalVerdict runs a script returning "" on success or a reason.
func (s *Session) evalVerdict(ctx context.Context, expr, sel string) error {
	var verdict string
	if err := s.run(ctx, chromedp.Evaluate(expr, &verdict)); err != nil {
		return err
	}

	if verdict != "" {
		return fmt.Errorf("%w: %q: %s", errSelector, sel, verdict)
	}

	return nil
}
