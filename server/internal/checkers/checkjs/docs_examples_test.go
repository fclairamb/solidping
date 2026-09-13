package checkjs

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dop251/goja"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhttp"
)

// docsPagePath is where the dedicated JS-check reference page lives, relative
// to this package directory. If a doc example rots (a typo, a stale field
// name, a script that no longer matches the runtime), THIS test — not a
// human copy-pasting the page into a check — is what catches it.
const docsPagePath = "../../../../web/docs/docs/features/javascript-checks.md"

// docExample is one fenced ```js block extracted from the doc page.
type docExample struct {
	// testName is the name from a preceding `<!-- test: name -->` comment, or
	// "" for an example that is only compile-checked.
	testName string
	script   string
}

// fenceStart / fenceEnd / testComment recognize the markdown conventions the
// doc page uses: a ```js fence, optionally preceded on its own line by
// `<!-- test: name -->`.
var testCommentRE = regexp.MustCompile(`^<!--\s*test:\s*([a-z0-9-]+)\s*-->$`)

// extractJSExamples parses docsPagePath for every fenced ```js block, pairing
// each with the `<!-- test: name -->` comment immediately preceding it, if
// any.
func extractJSExamples(t *testing.T) []docExample {
	t.Helper()

	raw, err := os.ReadFile(docsPagePath)
	require.NoError(t, err, "read %s", docsPagePath)

	lines := strings.Split(string(raw), "\n")

	var (
		examples []docExample
		pending  string
	)

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])

		if match := testCommentRE.FindStringSubmatch(line); match != nil {
			pending = match[1]

			continue
		}

		if line != "```js" {
			// A non-fence, non-comment line between a `<!-- test: -->` comment
			// and its fence (blank lines) is fine; anything else means the
			// comment did not immediately precede a js fence, so drop it.
			if line != "" {
				pending = ""
			}

			continue
		}

		var body strings.Builder

		for i++; i < len(lines) && strings.TrimSpace(lines[i]) != "```"; i++ {
			body.WriteString(lines[i])
			body.WriteString("\n")
		}

		examples = append(examples, docExample{testName: pending, script: body.String()})
		pending = ""
	}

	return examples
}

// TestDocExamplesCompile is the cheapest possible guard: every fenced js
// block on the page must be syntactically valid under the exact wrapper
// Execute() uses. A typo that breaks the page's copy-paste promise fails the
// build here, whether or not the example is also executed below.
func TestDocExamplesCompile(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	examples := extractJSExamples(t)
	r.NotEmpty(examples, "expected at least one fenced js example in %s", docsPagePath)

	for i, example := range examples {
		wrapped := "(function() {\n" + example.script + "\n})()"
		_, err := goja.Compile(fmt.Sprintf("doc-example-%d", i), wrapped, true)
		r.NoError(err, "example %d (test tag %q) failed to compile:\n%s", i, example.testName, example.script)
	}
}

// requireExample finds the one tagged example named name, failing the test if
// the doc page does not contain it — so renaming or deleting a `<!-- test:
// -->` comment on the page is caught here rather than silently un-testing the
// example it was attached to.
func requireExample(t *testing.T, examples []docExample, name string) string {
	t.Helper()

	for _, example := range examples {
		if example.testName == name {
			return example.script
		}
	}

	t.Fatalf("no doc example tagged `<!-- test: %s -->` found in %s", name, docsPagePath)

	return ""
}

// itemStore backs the create/read/delete fixture below.
type itemStore struct {
	mu     sync.Mutex
	items  map[string]string
	nextID int
}

func newItemStore() *itemStore {
	return &itemStore{items: make(map[string]string)}
}

// docsFixtureServer builds the httptest server every "run" example in the doc
// executes against. One server, one mux, one place to see what every example
// assumes about the world — matching env.BASE_URL for every tagged example.
//
// Credentials used throughout: username "alice", password "hunter2" — the
// exact values the doc-example executors below configure via env/secrets.
func docsFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()

	const (
		validUser  = "alice"
		validPass  = "hunter2"
		bearerTok  = "tok-xyz"
		csrfCookie = "csrf=abc123"
		sessionVal = "sess-1"
	)

	store := newItemStore()
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","latencyMs":42}`))
	})

	mux.HandleFunc("/login", func(w http.ResponseWriter, req *http.Request) {
		body := readAll(req)
		if strings.Contains(body, `"username":"`+validUser+`"`) && strings.Contains(body, `"password":"`+validPass+`"`) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"` + bearerTok + `"}`))

			return
		}

		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid credentials"}`))
	})

	mux.HandleFunc("/me", func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") == "Bearer "+bearerTok {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"user":"` + validUser + `"}`))

			return
		}

		w.WriteHeader(http.StatusUnauthorized)
	})

	mux.HandleFunc("/form/login", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			http.SetCookie(w, &http.Cookie{Name: "csrf", Value: "abc123", Path: "/"})
			_, _ = w.Write([]byte("login form"))

			return
		}

		if !strings.Contains(req.Header.Get("Cookie"), csrfCookie) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("missing csrf cookie"))

			return
		}

		body := readAll(req)
		values, err := parseFormBody(req.Context(), body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		if values.Get("username") == validUser && values.Get("password") == validPass {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: sessionVal, Path: "/"})
			w.Header().Set("Location", "/dashboard")
			w.WriteHeader(http.StatusFound)

			return
		}

		// A failed login is NOT a redirect — mirrors the real-world Keycloak
		// behavior the spec's problem statement describes: success is a 302,
		// failure is a 200 with an error page.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("invalid credentials"))
	})

	mux.HandleFunc("/dashboard", func(w http.ResponseWriter, req *http.Request) {
		cookie, err := req.Cookie("session")
		if err == nil && cookie.Value == sessionVal {
			_, _ = w.Write([]byte("welcome"))

			return
		}

		w.WriteHeader(http.StatusUnauthorized)
	})

	mux.HandleFunc("/basic", func(w http.ResponseWriter, req *http.Request) {
		user, pass, ok := req.BasicAuth()
		if ok && user == validUser && pass == validPass {
			_, _ = w.Write([]byte("ok"))

			return
		}

		w.WriteHeader(http.StatusUnauthorized)
	})

	mux.HandleFunc("/items", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)

			return
		}

		store.mu.Lock()
		store.nextID++
		id := strconv.Itoa(store.nextID)
		store.items[id] = readAll(req)
		store.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"` + id + `"}`))
	})

	mux.HandleFunc("/items/", func(w http.ResponseWriter, req *http.Request) {
		id := strings.TrimPrefix(req.URL.Path, "/items/")

		switch req.Method {
		case http.MethodGet:
			store.mu.Lock()
			body, ok := store.items[id]
			store.mu.Unlock()

			if !ok {
				w.WriteHeader(http.StatusNotFound)

				return
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"` + id + `","body":` + strconv.Quote(body) + `}`))
		case http.MethodDelete:
			store.mu.Lock()
			delete(store.items, id)
			store.mu.Unlock()

			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/agg/ok", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("/agg/bad", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server
}

// readAll reads a fixture request body. It deliberately does not use
// require/t: it runs inside an http.HandlerFunc on the server's own
// goroutine, not the test goroutine, and testify's require must never be
// called off the test goroutine. A read failure here just yields an empty
// body, which fails the surrounding assertion the normal way.
func readAll(req *http.Request) string {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return ""
	}

	return string(body)
}

// parseFormBody parses a application/x-www-form-urlencoded body without
// pulling in net/http's request-bound form parser (the fixture reads the body
// itself via readAll above, so req.ParseForm has nothing left to read).
func parseFormBody(ctx context.Context, body string) (formValues, error) {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, "http://example.invalid/", strings.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if err := req.ParseForm(); err != nil {
		return nil, err
	}

	return formValues(req.Form), nil
}

type formValues map[string][]string

func (v formValues) Get(key string) string {
	vals := v[key]
	if len(vals) == 0 {
		return ""
	}

	return vals[0]
}

// runDocExample executes a script exactly as Execute() would: same wrapper,
// same config shape.
func runDocExample(t *testing.T, script string, env, secrets map[string]string) *checkerdef.Result {
	t.Helper()

	checker := &JSChecker{}

	result, err := checker.Execute(context.Background(), &JSConfig{
		Script:  script,
		Timeout: 10 * time.Second,
		Env:     env,
		Secrets: secrets,
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	return result
}

// TestDocExampleJSONHealth runs the "json-health" example against a real
// JSON endpoint and checks it reports up with the payload's latency as a
// metric — proving the example actually parses the body it claims to.
func TestDocExampleJSONHealth(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	examples := extractJSExamples(t)
	script := requireExample(t, examples, "json-health")

	server := docsFixtureServer(t)

	result := runDocExample(t, script, map[string]string{"BASE_URL": server.URL}, nil)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
	r.InDelta(42, result.Metrics["latencyMs"], 0.001)
}

// TestDocExampleBearerChain is section 4's chaining example: log in, extract
// the token, use it on the next call. Credentials come from secrets, per the
// spec's own point that the example must NOT put the password in env.
func TestDocExampleBearerChain(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	examples := extractJSExamples(t)
	script := requireExample(t, examples, "bearer-chain")

	server := docsFixtureServer(t)

	result := runDocExample(t, script,
		map[string]string{"BASE_URL": server.URL, "USERNAME": "alice"},
		map[string]string{"PASSWORD": "hunter2"},
	)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
}

// TestDocExampleBearerChainRejectsWrongPassword is the negative the spec asks
// for by name: the SAME script, run against the SAME fixture, with the wrong
// password, must come back down — proving the example actually checks the
// credential rather than just checking that the transport works.
func TestDocExampleBearerChainRejectsWrongPassword(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	examples := extractJSExamples(t)
	script := requireExample(t, examples, "bearer-chain")

	server := docsFixtureServer(t)

	result := runDocExample(t, script,
		map[string]string{"BASE_URL": server.URL, "USERNAME": "alice"},
		map[string]string{"PASSWORD": "not-the-password"},
	)

	r.Equal("down", result.Status.String(), "output: %#v", result.Output)
}

// TestDocExampleFormSession is the cookie-jar example: http.session() must
// carry the csrf cookie set on the GET into the POST, or the fixture's 400
// (missing csrf cookie) would fire and the assertion would fail.
func TestDocExampleFormSession(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	examples := extractJSExamples(t)
	script := requireExample(t, examples, "form-session")

	server := docsFixtureServer(t)

	result := runDocExample(t, script,
		map[string]string{"BASE_URL": server.URL, "USERNAME": "alice"},
		map[string]string{"PASSWORD": "hunter2"},
	)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
}

// TestDocExampleFormSessionRejectsWrongPassword mirrors the bearer-chain
// negative for the session example.
func TestDocExampleFormSessionRejectsWrongPassword(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	examples := extractJSExamples(t)
	script := requireExample(t, examples, "form-session")

	server := docsFixtureServer(t)

	result := runDocExample(t, script,
		map[string]string{"BASE_URL": server.URL, "USERNAME": "alice"},
		map[string]string{"PASSWORD": "not-the-password"},
	)

	r.Equal("down", result.Status.String(), "output: %#v", result.Output)
}

// TestDocExampleFormManualCookie is the no-jar variant: it must reach the
// same "up" outcome by hand-copying Set-Cookie into the next request's
// Cookie header.
func TestDocExampleFormManualCookie(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	examples := extractJSExamples(t)
	script := requireExample(t, examples, "form-manual-cookie")

	server := docsFixtureServer(t)

	result := runDocExample(t, script,
		map[string]string{"BASE_URL": server.URL, "USERNAME": "alice"},
		map[string]string{"PASSWORD": "hunter2"},
	)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
}

// TestDocExampleBasicAuth proves the base64.encode-built Basic-auth header
// actually authenticates against a fixture that checks it with the standard
// library's own req.BasicAuth() — the two must agree on the encoding.
func TestDocExampleBasicAuth(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	examples := extractJSExamples(t)
	script := requireExample(t, examples, "basic-auth")

	server := docsFixtureServer(t)

	result := runDocExample(t, script,
		map[string]string{"BASE_URL": server.URL, "USERNAME": "alice"},
		map[string]string{"PASSWORD": "hunter2"},
	)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)

	wrong := runDocExample(t, script,
		map[string]string{"BASE_URL": server.URL, "USERNAME": "alice"},
		map[string]string{"PASSWORD": "wrong"},
	)
	r.Equal("down", wrong.Status.String(), "output: %#v", wrong.Output)
}

// TestDocExampleCreateReadDelete runs the multi-step workflow and confirms
// cleanup happened by reading the item back afterward and getting a 404 — a
// script that "forgot" the delete step would leave it retrievable.
func TestDocExampleCreateReadDelete(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	examples := extractJSExamples(t)
	script := requireExample(t, examples, "create-read-delete")

	server := docsFixtureServer(t)

	result := runDocExample(t, script, map[string]string{"BASE_URL": server.URL}, nil)
	r.Equal("up", result.Status.String(), "output: %#v", result.Output)

	// The cleanup step must have actually run: fetch whatever id the example
	// used is not observable from here directly, so instead assert on the
	// store being empty by re-running the same create against the fixture and
	// confirming ids are not reused/left dangling in a way that would make a
	// second create collide. A stronger, direct proof: hit the fixture's own
	// GET for id "1" (the first id this test's server ever issues) and expect
	// 404, since the example's own delete is the only thing that could clear
	// it.
	resp, err := http.Get(server.URL + "/items/1") //nolint:noctx // test-only, fixed local httptest URL
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusNotFound, resp.StatusCode, "the example's delete step did not run")
}

// TestDocExampleAggregateSubchecks proves solidping.http() wiring inside a
// doc example: two "up" sub-checks and one real 500 must still fold to
// "down" overall (worst status wins), against the REAL checkhttp checker —
// not a stub — so the example is honest about what solidping.http() does.
//
//nolint:paralleltest // mutates the package-level ResolveChecker global
func TestDocExampleAggregateSubchecks(t *testing.T) {
	r := require.New(t)

	examples := extractJSExamples(t)
	script := requireExample(t, examples, "aggregate-subchecks")

	server := docsFixtureServer(t)

	previous := ResolveChecker
	t.Cleanup(func() { ResolveChecker = previous })
	ResolveChecker = func(checkType checkerdef.CheckType) (checkerdef.Checker, checkerdef.Config, bool) {
		if checkType != checkerdef.CheckTypeHTTP {
			return nil, nil, false
		}

		return &checkhttp.HTTPChecker{}, &checkhttp.HTTPConfig{}, true
	}

	result := runDocExample(t, script, map[string]string{"BASE_URL": server.URL}, nil)

	r.Equal("down", result.Status.String(), "output: %#v", result.Output)
	r.Contains(result.Metrics, "check0Duration")
	r.Contains(result.Metrics, "check1Duration")
	r.Contains(result.Metrics, "check2Duration")
}

// TestSamplesCompile runs every sample GetSampleConfigs ships — including the
// two promoted from this page (bearer-chain, aggregate-subchecks) — through
// the same compile step as the doc page, so a sample cannot silently drift
// into a syntax error the picker would only surface when a user opened it.
func TestSamplesCompile(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &JSChecker{}
	samples := checker.GetSampleConfigs(nil)
	r.NotEmpty(samples)

	for _, sample := range samples {
		script, ok := sample.Config["script"].(string)
		r.True(ok, "sample %q has no script", sample.Name)

		wrapped := "(function() {\n" + script + "\n})()"
		_, err := goja.Compile(sample.Slug, wrapped, true)
		r.NoError(err, "sample %q failed to compile", sample.Name)
	}
}

// TestSamplesBearerChainAndAggregateRun executes the two promoted samples
// against the SAME fixture the doc page examples use, with the same
// credentials a user would substitute for the placeholders — keeping the
// sample picker honest, not just the docs page.
//
//nolint:paralleltest // mutates the package-level ResolveChecker global
func TestSamplesBearerChainAndAggregateRun(t *testing.T) {
	r := require.New(t)

	server := docsFixtureServer(t)

	checker := &JSChecker{}
	samples := checker.GetSampleConfigs(nil)

	var bearerScript, aggregateScript string

	for _, sample := range samples {
		script, _ := sample.Config["script"].(string)

		switch sample.Slug {
		case sampleBearerChainSlug:
			bearerScript = script
		case sampleAggregateSlug:
			aggregateScript = script
		}
	}

	r.NotEmpty(bearerScript, "expected a promoted bearer-token sample")
	r.NotEmpty(aggregateScript, "expected a promoted aggregate-subchecks sample")

	bearerResult := runDocExample(t, bearerScript,
		map[string]string{"BASE_URL": server.URL, "USERNAME": "alice"},
		map[string]string{"PASSWORD": "hunter2"},
	)
	r.Equal("up", bearerResult.Status.String(), "output: %#v", bearerResult.Output)

	previous := ResolveChecker
	t.Cleanup(func() { ResolveChecker = previous })
	ResolveChecker = func(checkType checkerdef.CheckType) (checkerdef.Checker, checkerdef.Config, bool) {
		if checkType != checkerdef.CheckTypeHTTP {
			return nil, nil, false
		}

		return &checkhttp.HTTPChecker{}, &checkhttp.HTTPConfig{}, true
	}

	aggResult := runDocExample(t, aggregateScript, map[string]string{
		"URL_1": server.URL + "/agg/ok",
		"URL_2": server.URL + "/agg/ok",
		"URL_3": server.URL + "/agg/bad",
	}, nil)
	r.Equal("down", aggResult.Status.String(), "output: %#v", aggResult.Output)
}

// TestBrowserLoginSampleMatchesTheDocExample is the drift guard for the third
// promoted sample.
//
// The other two are kept honest by being EXECUTED against the shared fixture;
// this one needs a real Chrome, which CI's backend job does not have. So the
// guard here is stronger rather than weaker: the sample's script must be the
// doc fence character for character. A change to either that is not made to
// both fails this test, not a user's check.
func TestBrowserLoginSampleMatchesTheDocExample(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	docScript := requireExample(t, extractJSExamples(t), "browser-login")

	checker := &JSChecker{}

	var sampleScript string

	for _, sample := range checker.GetSampleConfigs(nil) {
		if sample.Slug == sampleBrowserLoginSlug {
			sampleScript, _ = sample.Config["script"].(string)
		}
	}

	r.NotEmpty(sampleScript, "expected a promoted %s sample", sampleBrowserLoginSlug)
	r.Equal(docScript, sampleScript,
		"the %s sample and the doc example must be the same script", sampleBrowserLoginSlug)
}

// TestBrowserLoginSampleRunsAtTheBrowserFloor: a sample the server would
// refuse to save is worse than no sample. A script that opens a browser
// inherits the browser check's floor, so this one's period must clear it.
func TestBrowserLoginSampleRunsAtTheBrowserFloor(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &JSChecker{}

	for _, sample := range checker.GetSampleConfigs(nil) {
		script, _ := sample.Config["script"].(string)
		cfg := &JSConfig{Script: script}

		hint := cfg.MinPeriodHint()
		if hint == 0 {
			continue
		}

		r.Equal(sampleBrowserLoginSlug, sample.Slug,
			"only the browser sample should raise the period floor")
		r.GreaterOrEqual(sample.Period, hint,
			"sample %q would be refused by its own period floor", sample.Slug)
	}
}

// TestDocExampleBrowserLoginRunsAgainstARealBrowser is §9's end-to-end proof:
// the doc example, run verbatim, against a real headless Chrome and the
// httptest login fixture.
//
// It runs against SP_CHECKERS_BROWSER_CDP_URL, or a locally installed Chrome
// found the same way the checker's own exec path finds one, and skips with a
// visible reason when there is neither — CI's backend job has none.
//
//nolint:paralleltest // mutates the process-wide browser settings
func TestDocExampleBrowserLoginRunsAgainstARealBrowser(t *testing.T) {
	settings, ok := liveBrowserSettings()
	if !ok {
		t.Skip("no browser available: set SP_CHECKERS_BROWSER_CDP_URL or install a local Chrome/Chromium")
	}

	r := require.New(t)

	previous := checkbrowser.CurrentSettings()
	t.Cleanup(func() { checkbrowser.Configure(previous) })
	checkbrowser.Configure(settings)

	script := requireExample(t, extractJSExamples(t), "browser-login")

	fixture := browserFixtureServer(t)
	base := browserReachableURL(t, fixture.URL)

	checker := &JSChecker{}

	result, err := checker.Execute(t.Context(), &JSConfig{
		Script:  script,
		Timeout: 30 * time.Second,
		Env:     map[string]string{"BASE_URL": base, "USERNAME": "alice@acme.com"},
		Secrets: map[string]string{"PASSWORD": "hunter2"},
	})
	r.NoError(err)
	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
	r.Contains(result.Metrics, "loginMs")

	// Success path: the example's screenshot call is on the FAILING branch, so
	// nothing is attached here.
	if result.Diagnostics != nil {
		r.Nil(result.Diagnostics.Screenshot)
	}

	// The negative the example must actually be checking: a wrong password
	// never reaches the dashboard, so the same script reports down — with the
	// screenshot its failing branch took, kept because the verdict earns it.
	wrong, err := checker.Execute(t.Context(), &JSConfig{
		Script:  script,
		Timeout: 30 * time.Second,
		Env:     map[string]string{"BASE_URL": base, "USERNAME": "alice@acme.com"},
		Secrets: map[string]string{"PASSWORD": "not-the-password"},
	})
	r.NoError(err)
	r.Equal("down", wrong.Status.String(), "output: %#v", wrong.Output)
	r.Equal("login", wrong.Output["step"])
	r.NotNil(wrong.Diagnostics)
	r.NotNil(wrong.Diagnostics.Screenshot, "the failing branch's capture must be kept on a down verdict")
	r.NotEmpty(wrong.Diagnostics.Screenshot.PNG)
}

// liveBrowserSettings picks the browser backend this test should drive: a
// configured CDP endpoint first, else a Chrome installed on this machine,
// found with checkbrowser's OWN lookup so "the test ran" and "the checker
// would have worked" cannot disagree.
func liveBrowserSettings() (checkbrowser.Settings, bool) {
	if cdpURL := os.Getenv("SP_CHECKERS_BROWSER_CDP_URL"); cdpURL != "" {
		return checkbrowser.Settings{CDPURL: cdpURL}, true
	}

	if path := checkbrowser.FindChromeBinary(""); path != "" {
		return checkbrowser.Settings{ChromePath: path}, true
	}

	return checkbrowser.Settings{}, false
}

// browserReachableURL rewrites a local httptest URL into one BOTH the test
// process and the BROWSER can reach.
//
// A Chrome in a container cannot dial this process's 127.0.0.1, and the doc
// example deliberately uses ONE base URL for the page and for the http.get
// that follows it — so a name only the container resolves would break the
// second half. This machine's outbound-route address satisfies both, and
// SP_TEST_BROWSER_HOST overrides it for a setup where it does not (a Chrome
// sharing this network namespace wants 127.0.0.1).
func browserReachableURL(t *testing.T, rawURL string) string {
	t.Helper()

	alias := os.Getenv("SP_TEST_BROWSER_HOST")
	if alias == "" {
		alias = outboundHost(t)
	}

	_, port, err := net.SplitHostPort(strings.TrimPrefix(rawURL, "http://"))
	require.NoError(t, err, "unexpected httptest URL %q", rawURL)

	return "http://" + net.JoinHostPort(alias, port)
}

// outboundHost reports the local address this machine would use to reach the
// outside world. The UDP "connection" sends nothing — it only makes the kernel
// pick a route — so this works offline and costs a syscall.
func outboundHost(t *testing.T) string {
	t.Helper()

	conn, err := net.Dial("udp", "203.0.113.1:9") //nolint:noctx // no packet is sent; this only picks a route
	require.NoError(t, err, "cannot determine a browser-reachable host address")

	defer func() { _ = conn.Close() }()

	host, _, err := net.SplitHostPort(conn.LocalAddr().String())
	require.NoError(t, err)

	return host
}

// browserFixtureServer is the login app the browser example drives: a real
// form, a session cookie, a JS-rendered dashboard and an authenticated API.
// It listens on ALL interfaces so a browser outside this process can reach it.
func browserFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()

	const (
		validUser  = "alice@acme.com"
		validPass  = "hunter2"
		sessionVal = "browser-sess-1"
	)

	mux := http.NewServeMux()

	mux.HandleFunc("/login", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			_ = req.ParseForm()

			if req.Form.Get("email") == validUser && req.Form.Get("password") == validPass {
				http.SetCookie(w, &http.Cookie{Name: "session", Value: sessionVal, Path: "/"})
				http.Redirect(w, req, "/dashboard", http.StatusFound)

				return
			}

			_, _ = w.Write([]byte(`<html><body><p id="oops">invalid credentials</p></body></html>`))

			return
		}

		_, _ = w.Write([]byte(`<html><body><form method="post" action="/login">` +
			`<input id="email" name="email"><input id="password" name="password" type="password">` +
			`<button type="submit">Sign in</button></form></body></html>`))
	})

	// The dashboard renders its marker from JavaScript, which is exactly what
	// a plain browser check (or an http check) could not wait for.
	mux.HandleFunc("/dashboard", func(w http.ResponseWriter, req *http.Request) {
		if cookie, err := req.Cookie("session"); err != nil || cookie.Value != sessionVal {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		_, _ = w.Write([]byte(`<html><title>Dash</title><body><div id="app"></div><script>` +
			`setTimeout(function () { document.getElementById("app").innerHTML =` +
			` '<h1 data-testid="dashboard">welcome</h1>'; }, 150);</script></body></html>`))
	})

	mux.HandleFunc("/api/me", func(w http.ResponseWriter, req *http.Request) {
		if cookie, err := req.Cookie("session"); err != nil || cookie.Value != sessionVal {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"user":"alice"}`))
	})

	listener, err := net.Listen("tcp", "0.0.0.0:0") //nolint:noctx // fixture the browser must reach
	require.NoError(t, err)

	server := httptest.NewUnstartedServer(mux)
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()

	t.Cleanup(server.Close)

	return server
}
