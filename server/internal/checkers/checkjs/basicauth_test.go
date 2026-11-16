package checkjs

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser"
)

// TestBasicAuthBindingForwardsArgs: page.basicAuth hands the session the
// username, the password and the origin option as given, and answers { ok }.
//
//nolint:paralleltest // mutates the package-level OpenBrowser seam
func TestBasicAuthBindingForwardsArgs(t *testing.T) {
	r := require.New(t)

	session := &fakeSession{}
	installFakeBrowser(t, session)

	checker := &JSChecker{}
	result, err := checker.Execute(t.Context(), &JSConfig{
		Script: `
var page = browser.open();
var first = page.basicAuth(secrets.USER, secrets.PASSWORD);
var second = page.basicAuth("bob", "pw", { origin: "https://staging.acme.com" });
return { status: "up", output: { first: first.ok, second: second.ok } };
`,
		Secrets: map[string]string{"USER": "alice", "PASSWORD": "s3cr3t-value"},
		Timeout: 5 * time.Second,
	})
	r.NoError(err)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
	r.Equal(true, result.Output["first"])
	r.Equal(true, result.Output["second"])
	r.Equal([]string{"alice:s3cr3t-value@", "bob:pw@https://staging.acme.com"}, session.basicAuths)

	encoded, err := json.Marshal(result)
	r.NoError(err)
	r.NotContains(string(encoded), "s3cr3t-value", "the password must appear nowhere in the result")
}

// TestBasicAuthBindingRefusalIsATargetFailure: a session error comes back as
// { ok: false, error }, like every other page method.
//
//nolint:paralleltest // mutates the package-level OpenBrowser seam
func TestBasicAuthBindingRefusalIsATargetFailure(t *testing.T) {
	r := require.New(t)

	installFakeBrowser(t, &fakeSession{basicAuthErr: errors.New("basicAuth: origin must be an http or https origin")})

	result := runBrowserScript(t, `
var page = browser.open();
var res = page.basicAuth("u", "p", { origin: "ftp://acme.com" });
return { status: "up", output: { ok: res.ok, error: res.error } };
`, 5*time.Second)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
	r.Equal(false, result.Output["ok"])
	r.Contains(result.Output["error"], "origin must be")
}

// TestBasicAuthBindingAfterCloseThrows: like every other page method, a call
// on a closed page is infrastructure and throws.
//
//nolint:paralleltest // mutates the package-level OpenBrowser seam
func TestBasicAuthBindingAfterCloseThrows(t *testing.T) {
	r := require.New(t)

	installFakeBrowser(t, &fakeSession{})

	result := runBrowserScript(t, `
var page = browser.open();
page.close();
page.basicAuth("u", "p");
return { status: "up" };
`, 5*time.Second)

	r.Equal("error", result.Status.String(), "output: %#v", result.Output)
}

// TestBasicAuthBindingRejectsUndefinedSecrets: a misspelled secret is a
// script bug that throws, not a request sent with the password "undefined".
//
//nolint:paralleltest // mutates the package-level OpenBrowser seam
func TestBasicAuthBindingRejectsUndefinedSecrets(t *testing.T) {
	r := require.New(t)

	session := &fakeSession{}
	installFakeBrowser(t, session)

	result := runBrowserScript(t, `
var page = browser.open();
page.basicAuth("alice", secrets.MISSPELLED);
return { status: "up" };
`, 5*time.Second)

	r.Equal("error", result.Status.String(), "output: %#v", result.Output)
	r.Contains(result.Output["error"], "password must be a string")
	r.Empty(session.basicAuths, "nothing must reach the session")
}

// TestDocExampleBrowserBasicAuthRunsAgainstARealBrowser runs the documented
// example against a real Chrome and a Basic-auth site: the right secrets are
// `up`, wrong ones are `down` with an error naming the origin, and the
// password appears nowhere in either result.
//
//nolint:paralleltest // mutates the process-wide browser settings
func TestDocExampleBrowserBasicAuthRunsAgainstARealBrowser(t *testing.T) {
	settings, ok := liveBrowserSettings()
	if !ok {
		t.Skip("no browser available: set SP_CHECKERS_BROWSER_CDP_URL or install a local Chrome/Chromium")
	}

	previous := checkbrowser.CurrentSettings()
	t.Cleanup(func() { checkbrowser.Configure(previous) })
	checkbrowser.Configure(settings)

	script := requireExample(t, extractJSExamples(t), "browser-basic-auth")

	listener, err := net.Listen("tcp", "0.0.0.0:0") //nolint:noctx // fixture the browser must reach
	require.NoError(t, err)

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		user, pass, hasAuth := req.BasicAuth()
		if !hasAuth || user != "alice" || pass != "pw-s3cr3t-value" {
			w.Header().Set("WWW-Authenticate", `Basic realm="staging"`)
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		_, _ = w.Write([]byte(`<html><body><h1>welcome</h1></body></html>`))
	}))
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	base := browserReachableURL(t, server.URL)

	for _, tc := range []struct {
		password string
		status   string
	}{
		{password: "pw-s3cr3t-value", status: "up"},
		{password: "wrong-s3cr3t-value", status: "down"},
	} {
		r := require.New(t)

		result, execErr := (&JSChecker{}).Execute(t.Context(), &JSConfig{
			Script:  script,
			Timeout: 30 * time.Second,
			Env:     map[string]string{"BASE_URL": base},
			Secrets: map[string]string{"USER": "alice", "PASSWORD": tc.password},
		})
		r.NoError(execErr)
		r.Equal(tc.status, result.Status.String(), "output: %#v", result.Output)

		if tc.status == "down" {
			r.Equal("basic auth rejected by "+base, result.Output["error"])
		}

		encoded, marshalErr := json.Marshal(result)
		r.NoError(marshalErr)
		r.NotContains(string(encoded), tc.password, "the password must appear nowhere in the result")
	}
}
