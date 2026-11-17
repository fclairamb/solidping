package checkbrowser

import (
	"net"
	"net/http"
	neturl "net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/egress"
)

func TestParseSelector(t *testing.T) {
	t.Parallel()

	cases := map[string][2]string{
		"button":                 {"css", "button"},
		"css=button":             {"css", "button"},
		"text=Sign in":           {"text", "Sign in"},
		"label=Email":            {"label", "Email"},
		"name=q":                 {"name", "q"},
		"auto=START":             {"auto", "START"},
		"a[href='x=y']":          {"css", "a[href='x=y']"},
		"unknown=whatever":       {"css", "unknown=whatever"},
		"text=name=nested":       {"text", "name=nested"},
		"#text=not-a-kind":       {"css", "#text=not-a-kind"},
		"aria/Sign in":           {"aria", "Sign in"},
		`aria/Go[role="button"]`: {"aria", `Go[role="button"]`},
		"xpath///button[2]":      {"xpath", "//button[2]"},
		"pierce/#x":              {"pierce", "#x"},
		"text/Sign in":           {"text", "Sign in"},
	}

	for in, want := range cases {
		kind, value := parseSelector(in)
		require.Equal(t, want, [2]string{kind, value}, in)
	}
}

const selectorFixture = `<html><head><style>h1 { color: red }</style></head><body>
<h1>Welcome</h1>
<form id="f" onsubmit="document.title='submitted'; return false">
<label for="em">Email address</label><input id="em" name="email" placeholder="you@acme.com">
<label>Password <input id="pw" name="password" type="password"></label>
<input id="aria" aria-label="Search the site">
<input id="ph" placeholder="Phone number">
<label><input id="cb" type="checkbox" name="remember"> Remember me</label>
<select id="sel" name="country"><option value="fr">France</option><option value="de">Germany</option></select>
<select id="rsel" name="framework"><option value="a">Alpha</option><option value="b">Beta</option></select>
<button type="submit" id="go">Sign in</button>
</form>
<button id="a1">Save</button><button id="a2">Save draft</button>
<button id="dup">Twice</button><button id="duph" style="display:none">Twice</button>
<button id="two1">Same</button><button id="two2">Same</button>
<input type="submit" id="sub" value="Send it">
<p id="own">Hello <b>big</b> world</p>
<script>
var body = document.body;
document.getElementById('sel').addEventListener('change', function(e){
  body.setAttribute('data-sel', e.target.value);
});
document.getElementById('cb').addEventListener('click', function(){
  body.setAttribute('data-clicks', (+body.getAttribute('data-clicks')||0)+1);
});
// A React-like controlled select: an instance-level value property tracks
// every write made through it (as React's value tracker does), and the
// listener only acts when the NATIVE value differs from the tracked one.
(function(){
  var el = document.getElementById('rsel');
  var native = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value');
  var tracked = native.get.call(el);
  Object.defineProperty(el, 'value', {
    configurable: true,
    get: function(){ return native.get.call(this); },
    set: function(v){ tracked = String(v); native.set.call(this, v); }
  });
  function onEvent(){
    var current = native.get.call(el);
    if (current !== tracked) {
      tracked = current;
      body.setAttribute('data-react', current);
    }
  }
  el.addEventListener('input', onEvent);
  el.addEventListener('change', onEvent);
})();
</script>
</body></html>`

//nolint:paralleltest // mutates the process-wide settings
func TestSelectorPrefixesLive(t *testing.T) {
	settings, ok := liveBrowserSettings()
	if !ok {
		t.Skip(liveBrowserSkipReason)
	}

	r := require.New(t)

	withSettings(t, settings)
	widenChromeStart(t)

	fixture := liveFixtureServer(t)
	mux, isMux := fixture.Config.Handler.(*http.ServeMux)
	r.True(isMux)
	mux.HandleFunc("/sel", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(selectorFixture))
	})

	base := browserReachableURL(t, fixture.URL)

	ctx, cancel := contextWithTimeout(t, 60*time.Second)
	defer cancel()

	session, err := Open(ctx)
	r.NoError(err)

	defer session.Close()

	_, err = session.Navigate(ctx, base+"/sel")
	r.NoError(err)

	text := func(sel string) string {
		got, textErr := session.Text(ctx, sel)
		r.NoError(textErr, sel)

		return got
	}

	// Unprefixed stays CSS, even with the inline <style> naming "h1".
	r.Equal("Welcome", text("h1"))
	r.Equal("Welcome", text("css=h1"))
	// Each prefix.
	r.Equal("Sign in", text("text=Sign in"))
	r.NoError(session.Fill(ctx, "label=Email address", "a@acme.com"))
	r.NoError(session.Fill(ctx, "label=Password", "pw"))
	r.NoError(session.Fill(ctx, "label=Search the site", "x"))
	r.NoError(session.Fill(ctx, "label=Phone number", "123"))
	r.NoError(session.Fill(ctx, "name=email", "b@acme.com"))
	r.NoError(session.WaitVisible(ctx, "name=country"))
	r.NoError(session.Click(ctx, "text=Send it"))

	// Exact beats contains: "Save" is also contained in "Save draft".
	r.NoError(session.Click(ctx, "text=save"))
	// Text of a non-clickable element, from its own text.
	r.Equal("Hello big world", text("text=hello"))
	// Hidden duplicate ignored.
	r.NoError(session.Click(ctx, "text=Twice"))

	// Ambiguous and missing.
	err = session.Click(ctx, "text=Same")
	r.ErrorContains(err, `text="Same" matches 2 elements, make it more specific`)
	err = session.Click(ctx, "text=Nope")
	r.ErrorContains(err, `no element matches text="Nope"`)

	// auto= order: CSS first, then name, label, text.
	r.Equal("Welcome", text("auto=h1"))
	r.NoError(session.Fill(ctx, "auto=email", "c@acme.com"))
	r.NoError(session.Fill(ctx, "auto=Email address", "d@acme.com"))
	r.Equal("Sign in", text("auto=Sign in"))

	// The tag is released.
	var tagged any

	tagged, err = session.Evaluate(ctx, `document.querySelectorAll("[data-sp-target]").length`)
	r.NoError(err)
	r.EqualValues(0, tagged)

	// waitFor with a prefix polls until it shows up.
	_, err = session.Evaluate(ctx, `setTimeout(function(){
var b=document.createElement("button");b.textContent="Late one";document.body.appendChild(b)}, 300)`)
	r.NoError(err)
	r.NoError(session.WaitVisible(ctx, "text=Late one"))

	// check / uncheck: click only when the state differs.
	r.NoError(session.Check(ctx, "label=Remember me"))
	r.NoError(session.Check(ctx, "label=Remember me"))

	clicks, err := session.Evaluate(ctx, `document.body.getAttribute("data-clicks")`)
	r.NoError(err)
	r.Equal("1", clicks)
	r.NoError(session.Uncheck(ctx, "name=remember"))
	r.NoError(session.Uncheck(ctx, "name=remember"))

	clicks, err = session.Evaluate(ctx, `document.body.getAttribute("data-clicks")`)
	r.NoError(err)
	r.Equal("2", clicks)

	// select by value then text, firing change.
	r.NoError(session.Select(ctx, "name=country", "Germany"))

	got, err := session.Evaluate(ctx, `document.body.getAttribute("data-sel")`)
	r.NoError(err)
	r.Equal("de", got)
	r.NoError(session.Select(ctx, "css=#sel", "fr"))
	r.ErrorContains(session.Select(ctx, "css=#sel", "Spain"), "no option matches")

	// a framework-controlled select (value tracker) must notice the change.
	r.NoError(session.Select(ctx, "name=framework", "Beta"))

	react, err := session.Evaluate(ctx, `document.body.getAttribute("data-react")`)
	r.NoError(err)
	r.Equal("b", react)

	// submit through requestSubmit, from the form or a field in it.
	r.NoError(session.Submit(ctx, "label=Email address"))

	title, err := session.Evaluate(ctx, `document.title`)
	r.NoError(err)
	r.Equal("submitted", title)
}

// The selector machinery tags and untags elements through page JS. Under an
// enforcing egress guard that must stay invisible: no refusal is recorded and
// resolve/release keep working (the guard only judges network responses).
//
//nolint:paralleltest // mutates the process-wide settings
func TestSelectorTaggingDoesNotTripEgressGuard(t *testing.T) {
	settings, ok := liveBrowserSettings()
	if !ok {
		t.Skip(liveBrowserSkipReason)
	}

	r := require.New(t)

	withSettings(t, settings)
	widenChromeStart(t)

	fixture := liveFixtureServer(t)
	mux, isMux := fixture.Config.Handler.(*http.ServeMux)
	r.True(isMux)
	mux.HandleFunc("/sel", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(selectorFixture))
	})

	base := browserReachableURL(t, fixture.URL)
	parsed, err := neturl.Parse(base)
	r.NoError(err)

	fixtureIP := net.ParseIP(parsed.Hostname())
	r.NotNil(fixtureIP, "the fixture URL host must be an IP literal")

	// Enforcing guard (private ranges denied); only the fixture is public.
	guard := egress.New(false, egress.WithPublicOverride(fixtureIP))
	r.True(guard.Enforcing())

	baseCtx, cancel := contextWithTimeout(t, 60*time.Second)
	defer cancel()

	ctx := egress.WithGuard(baseCtx, guard)

	session, err := Open(ctx)
	r.NoError(err)

	defer session.Close()

	_, err = session.Navigate(ctx, base+"/sel")
	r.NoError(err)

	got, err := session.Text(ctx, "text=Sign in")
	r.NoError(err)
	r.Equal("Sign in", got)
	r.NoError(session.Fill(ctx, "label=Email address", "a@acme.com"))
	r.NoError(session.Click(ctx, "text=Send it"))

	// Tagging was released and the guard recorded nothing.
	tagged, err := session.Evaluate(ctx, `document.querySelectorAll("[data-sp-target]").length`)
	r.NoError(err)
	r.EqualValues(0, tagged)
	r.NoError(session.deniedByEgress())
}

func TestSelectorList(t *testing.T) {
	t.Parallel()

	list, isList := parseSelectorList(EncodeSelectorList([]string{"aria/Go", "#a > b", `x[y="z"]`}))
	require.True(t, isList)
	require.Equal(t, []string{"aria/Go", "#a > b", `x[y="z"]`}, list)

	// CSS attribute selectors starting with "[" are not lists.
	for _, css := range []string{"[name=q]", `[data-x="1"]`, "button", "[]"} {
		got, isList := parseSelectorList(css)
		require.False(t, isList, css)
		require.Equal(t, []string{css}, got)
	}

	require.True(t, needsResolver(`["#a"]`))
	require.True(t, needsResolver("aria/x"))
	require.False(t, needsResolver("#a"))
}

func TestAriaRole(t *testing.T) {
	t.Parallel()

	name, role := ariaRole(`Sign in[role="button"]`)
	require.Equal(t, "Sign in", name)
	require.Equal(t, "button", role)

	name, role = ariaRole("Sign in")
	require.Equal(t, "Sign in", name)
	require.Empty(t, role)
}

const recorderFixture = `<html><body>
<button id="r1" aria-label="Log in">x</button>
<a href="#" id="r2">Log in</a>
<h2 role="heading">Title</h2>
<div id="host"></div>
<button id="amb1">Dup</button><button id="amb2">Dup</button>
<script>
var root = document.getElementById("host").attachShadow({mode: "open"});
root.innerHTML = '<button id="deep">Deep action</button>';
root.getElementById("deep").addEventListener("click", function(){ document.title = "deep-clicked"; });
document.getElementById("r2").addEventListener("click", function(){ document.title = "link-clicked"; });
document.getElementById("r1").addEventListener("click", function(){ document.title = "button-clicked"; });
</script></body></html>`

//nolint:paralleltest // mutates the process-wide settings
func TestRecorderSelectorsLive(t *testing.T) {
	settings, ok := liveBrowserSettings()
	if !ok {
		t.Skip(liveBrowserSkipReason)
	}

	r := require.New(t)

	withSettings(t, settings)
	widenChromeStart(t)

	fixture := liveFixtureServer(t)
	mux, isMux := fixture.Config.Handler.(*http.ServeMux)
	r.True(isMux)
	mux.HandleFunc("/rec", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(recorderFixture))
	})

	base := browserReachableURL(t, fixture.URL)

	ctx, cancel := contextWithTimeout(t, 60*time.Second)
	defer cancel()

	session, err := Open(ctx)
	r.NoError(err)

	defer session.Close()

	_, err = session.Navigate(ctx, base+"/rec")
	r.NoError(err)

	title := func() any {
		got, evalErr := session.Evaluate(ctx, `document.title`)
		r.NoError(evalErr)

		return got
	}

	// aria/ with a role picks the button, not the link of the same name.
	r.NoError(session.Click(ctx, `aria/Log in[role="button"]`))
	r.Equal("button-clicked", title())
	r.NoError(session.Click(ctx, `aria/Log in[role="link"]`))
	r.Equal("link-clicked", title())
	// Without a role both match: ambiguous.
	r.ErrorContains(session.Click(ctx, "aria/Log in"), "matches 2 elements")
	r.ErrorContains(session.Click(ctx, "aria/Nothing like it"), "no element matches")

	// xpath/ and text/.
	r.NoError(session.Click(ctx, "xpath///button[@id='r1']"))
	r.Equal("button-clicked", title())
	r.NoError(session.Click(ctx, "text/Log in"))

	// pierce/ reaches inside an open shadow root; plain CSS does not.
	r.NoError(session.Click(ctx, "pierce/#deep"))
	r.Equal("deep-clicked", title())
	r.Error(session.Click(ctx, "pierce/#absent"))

	// Fallback list: stale first, ambiguous first, all failing.
	r.NoError(session.Click(ctx, EncodeSelectorList([]string{"#gone", "#r2"})))
	r.Equal("link-clicked", title())
	r.NoError(session.Click(ctx, EncodeSelectorList([]string{"text=Dup", "#r1"})))
	r.Equal("button-clicked", title())

	err = session.Click(ctx, EncodeSelectorList([]string{"#gone", "text=Dup", "aria/Nothing"}))
	r.ErrorContains(err, "#gone (0 matches)")
	r.ErrorContains(err, "text=Dup (2 matches)")
	r.ErrorContains(err, "aria/Nothing (0 matches)")

	// waitFor over a list.
	r.NoError(session.WaitVisible(ctx, EncodeSelectorList([]string{"#gone", "#r1"})))
}

// TestEnsureCheckedClicksOnlyWhenTheStateDiffers is the Chrome-free proof of
// the check/uncheck rule: an already-checked box is not clicked.
func TestEnsureCheckedClicksOnlyWhenTheStateDiffers(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		start      bool
		want       bool
		wantClicks int
	}{
		{"already checked", true, true, 0},
		{"already unchecked", false, false, 0},
		{"needs check", false, true, 1},
		{"needs uncheck", true, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			state, clicks := tc.start, 0
			err := ensureChecked("#cb", tc.want,
				func() (*bool, error) { v := state; return &v, nil },
				func() error { clicks++; state = !state; return nil },
			)
			require.NoError(t, err)
			require.Equal(t, tc.wantClicks, clicks)
		})
	}

	// A click that does not flip the box fails; a vanished element fails.
	stuck := false
	err := ensureChecked("#cb", true,
		func() (*bool, error) { return &stuck, nil }, func() error { return nil })
	require.ErrorContains(t, err, "is not checked after the click")

	err = ensureChecked("#cb", true,
		func() (*bool, error) { return nil, nil }, func() error { return nil }) //nolint:nilnil // a gone element
	require.ErrorContains(t, err, "is gone")
}
