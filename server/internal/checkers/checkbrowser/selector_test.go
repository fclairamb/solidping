package checkbrowser

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseSelector(t *testing.T) {
	t.Parallel()

	cases := map[string][2]string{
		"button":           {"css", "button"},
		"css=button":       {"css", "button"},
		"text=Sign in":     {"text", "Sign in"},
		"label=Email":      {"label", "Email"},
		"name=q":           {"name", "q"},
		"auto=START":       {"auto", "START"},
		"a[href='x=y']":    {"css", "a[href='x=y']"},
		"unknown=whatever": {"css", "unknown=whatever"},
		"text=name=nested": {"text", "name=nested"},
		"#text=not-a-kind": {"css", "#text=not-a-kind"},
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

	// submit through requestSubmit, from the form or a field in it.
	r.NoError(session.Submit(ctx, "label=Email address"))

	title, err := session.Evaluate(ctx, `document.title`)
	r.NoError(err)
	r.Equal("submitted", title)
}
