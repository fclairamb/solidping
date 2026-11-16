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

	"github.com/chromedp/chromedp"
)

// Selector kinds a script can prefix its selector with, Playwright-style.
const (
	kindCSS   = "css"
	kindText  = "text"
	kindLabel = "label"
	kindName  = "name"
	kindAuto  = "auto"
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

	return kindCSS, sel
}

// resolverJS finds the element a non-CSS selector designates, keeps only the
// VISIBLE ones, requires exactly one, tags it with a per-call attribute and
// returns {sel} (the CSS selector of the tag) or {err}. Evaluated with
// (kind, value, nonce) appended as the call arguments.
const resolverJS = `(function(kind, value, nonce) {
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
  var finders = {css: byCSS, name: byName, label: byLabel, text: byText};
  var found = [];
  if (kind === "auto") {
    var order = ["css", "name", "label", "text"];
    for (var i = 0; i < order.length && !found.length; i++) found = finders[order[i]]();
  } else {
    found = finders[kind]();
  }
  var shown = kind + "=" + JSON.stringify(value);
  if (found.length === 0) return {err: "no element matches " + shown};
  if (found.length > 1) {
    return {err: shown + " matches " + found.length + " elements, make it more specific"};
  }
  found[0].setAttribute("data-sp-target", nonce);
  return {sel: '[data-sp-target="' + nonce + '"]'};
})`

// resolveSelector turns a prefixed selector into a CSS one by tagging the
// matching element. The returned release removes the tag (best effort); for a
// plain CSS selector nothing is tagged and release is a no-op.
func (s *Session) resolveSelector(ctx context.Context, sel string) (string, func(), error) {
	kind, value := parseSelector(sel)
	if kind == kindCSS {
		return value, func() {}, nil
	}

	nonce, err := newNonce()
	if err != nil {
		return "", nil, err
	}

	args, err := json.Marshal([]string{kind, value, nonce})
	if err != nil {
		return "", nil, fmt.Errorf("encoding selector: %w", err)
	}

	// The args slice is spread into the call: JSON array -> arguments.
	expr := resolverJS + ".apply(null, " + string(args) + ")"

	var res struct {
		Sel string `json:"sel"`
		Err string `json:"err"`
	}

	if runErr := s.run(ctx, chromedp.Evaluate(expr, &res)); runErr != nil {
		return "", nil, runErr
	}

	if res.Err != "" {
		return "", nil, fmt.Errorf("%w: %s", errSelector, res.Err)
	}

	release := func() {
		cleanup := `document.querySelectorAll(` + jsString(res.Sel) +
			`).forEach(function(e){e.removeAttribute("data-sp-target")})`

		var ignored any

		_ = s.run(ctx, chromedp.Evaluate(cleanup, &ignored))
	}

	return res.Sel, release, nil
}

// withTarget resolves sel and runs action on the resulting CSS selector.
func (s *Session) withTarget(ctx context.Context, sel string, action func(css string) error) error {
	css, release, err := s.resolveSelector(ctx, sel)
	if err != nil {
		return err
	}

	defer release()

	return action(css)
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
	return s.withTarget(ctx, sel, func(css string) error {
		read := `(function(){var e=document.querySelector(` + jsString(css) + `);` +
			`return e ? !!e.checked : null})()`

		var state *bool
		if err := s.run(ctx, chromedp.Evaluate(read, &state)); err != nil {
			return err
		}

		if state == nil {
			return fmt.Errorf("%w: %q is gone", errSelector, sel)
		}

		if *state != want {
			if err := s.run(ctx, chromedp.Click(css, chromedp.ByQuery)); err != nil {
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
const selectJS = `(function(css, want) {
  var e = document.querySelector(css);
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
	return s.withTarget(ctx, sel, func(css string) error {
		return s.evalVerdict(ctx, selectJS+"("+jsString(css)+","+jsString(option)+")", sel)
	})
}

// submitJS submits the form (or the form owning the element) through
// requestSubmit, so validation and submit handlers run.
const submitJS = `(function(css) {
  var e = document.querySelector(css);
  if (!e) return "element is gone";
  var f = e.tagName === "FORM" ? e : (e.form || e.closest("form"));
  if (!f) return "not in a form";
  f.requestSubmit();
  return "";
})`

// Submit submits the form designated by sel, or owning the element sel does.
func (s *Session) Submit(ctx context.Context, sel string) error {
	return s.withTarget(ctx, sel, func(css string) error {
		return s.evalVerdict(ctx, submitJS+"("+jsString(css)+")", sel)
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
