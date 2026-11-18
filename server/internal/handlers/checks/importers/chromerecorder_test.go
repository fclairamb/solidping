package importers_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checkjsconfig "github.com/fclairamb/solidping/server/internal/checkers/checkjs/config"
	"github.com/fclairamb/solidping/server/internal/handlers/checks/importers"
)

func TestChromeRecorderLoginGolden(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	conv := &importers.ChromeRecorderConverter{}
	r.Equal("chrome-recorder", conv.Source())

	result, err := conv.Convert(readFixture(t, "chrome-recorder-login.json"))
	r.NoError(err)
	r.Len(result.Document.Checks, 1)
	assertGolden(t, "chrome-recorder-login", result)

	check := result.Document.Checks[0]
	r.True(check.Enabled)
	r.Equal("js", check.Type)

	// The generated script passes the JS check's own config validation.
	spec := &checkerdef.CheckSpec{Name: check.Name, Slug: check.Slug, Config: check.Config}
	r.NoError(checkjsconfig.ValidateSpec(spec))
}

func TestChromeRecorderPasswordMovedToSecrets(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result, err := (&importers.ChromeRecorderConverter{}).Convert(readFixture(t, "chrome-recorder-login.json"))
	r.NoError(err)

	cfg := result.Document.Checks[0].Config
	script, ok := cfg["script"].(string)
	r.True(ok)
	r.NotContains(script, "s3cret-Passw0rd!")
	r.Contains(script, "secrets.RECORDER_SECRET_1")
	r.Contains(script, "ops@acme.example", "non-password values stay in clear")

	secrets, ok := cfg["secrets"].(map[string]any)
	r.True(ok)
	r.Equal("s3cret-Passw0rd!", secrets["RECORDER_SECRET_1"])

	var found bool

	for _, warning := range result.Warnings {
		r.NotContains(warning.Message, "s3cret-Passw0rd!")

		if warning.Field == "secrets" {
			found = true

			r.Contains(warning.Message, "RECORDER_SECRET_1")
		}
	}

	r.True(found, "a secrets warning is expected")
}

// recorderChangeFlow is a one-field flow typing value into the selectors.
func recorderChangeFlow(selectors, value string) []byte {
	return []byte(`{"title": "Acme typed", "steps": [
	  {"type": "navigate", "url": "https://app.acme.example/"},
	  {"type": "change", "value": "` + value + `", "selectors": ` + selectors + `}
	]}`)
}

func TestChromeRecorderTypedValueRule(t *testing.T) {
	t.Parallel()

	t.Run("password type selector goes to secrets", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		result, err := (&importers.ChromeRecorderConverter{}).Convert(
			recorderChangeFlow(`[["#cred"], ["input[type=\"password\"]"]]`, "hunter2-acme"))
		r.NoError(err)

		cfg := result.Document.Checks[0].Config
		script, ok := cfg["script"].(string)
		r.True(ok)
		r.NotContains(script, "hunter2-acme")
		r.Contains(script, "secrets.RECORDER_SECRET_1")

		secrets, ok := cfg["secrets"].(map[string]any)
		r.True(ok)
		r.Equal("hunter2-acme", secrets["RECORDER_SECRET_1"])

		for _, warning := range result.Warnings {
			r.NotContains(warning.Message, "stayed in the script")
		}
	})

	t.Run("credential-looking value goes to secrets", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		result, err := (&importers.ChromeRecorderConverter{}).Convert(
			recorderChangeFlow(`[["#field-3"]]`, "sk9Fq2LxP7vRz4TbW1mN8cYd"))
		r.NoError(err)

		script, ok := result.Document.Checks[0].Config["script"].(string)
		r.True(ok)
		r.NotContains(script, "sk9Fq2LxP7vRz4TbW1mN8cYd")
		r.Contains(script, "secrets.RECORDER_SECRET_1")
	})

	t.Run("plain value stays in the script with a warning", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		result, err := (&importers.ChromeRecorderConverter{}).Convert(recorderChangeFlow(`[["#search"]]`, "hello"))
		r.NoError(err)

		cfg := result.Document.Checks[0].Config
		script, ok := cfg["script"].(string)
		r.True(ok)
		r.Contains(script, `"hello"`)
		r.NotContains(cfg, "secrets")

		var found bool

		for _, warning := range result.Warnings {
			if strings.Contains(warning.Message, "1 typed value(s) stayed in the script") {
				found = true
			}
		}

		r.True(found, "the stayed-in-the-script warning is expected")
	})
}

func TestChromeRecorderUnsupportedStepsPauseTheCheck(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result, err := (&importers.ChromeRecorderConverter{}).Convert(readFixture(t, "chrome-recorder-unsupported.json"))
	r.NoError(err)
	r.False(result.Document.Checks[0].Enabled)

	script, ok := result.Document.Checks[0].Config["script"].(string)
	r.True(ok)
	r.Contains(script, "// TODO step 2")
	r.Contains(script, "// TODO step 3")
	r.Contains(script, "// TODO step 4")
	r.Contains(script, "// TODO step 5")
	r.Contains(script, "// TODO step 6", "a selector crossing a shadow root leaves nothing usable")

	assertGolden(t, "chrome-recorder-unsupported", result)
}

func TestChromeRecorderInvalidInput(t *testing.T) {
	t.Parallel()

	conv := &importers.ChromeRecorderConverter{}

	for name, input := range map[string]string{
		"malformed":     `{"title": `,
		"no steps":      `{"title": "x", "steps": []}`,
		"not navigate":  `{"title": "x", "steps": [{"type": "click", "selectors": [["#a"]]}]}`,
		"only viewport": `{"title": "x", "steps": [{"type": "setViewport", "width": 1, "height": 1}]}`,
		"step w/o type": `{"title": "x", "steps": [{"url": "https://a.example"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := conv.Convert([]byte(input))
			require.ErrorIs(t, err, importers.ErrRecorderInvalid)
		})
	}
}

func TestChromeRecorderWaitForElementAssertions(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	input := `{"title": "w", "steps": [
	  {"type": "navigate", "url": "https://a.example"},
	  {"type": "waitForElement", "selectors": [["#row"]], "count": 3, "operator": "==",
	   "attributes": {"data-state": "ready"}, "properties": {"innerText": "ok"}},
	  {"type": "waitForElement", "selectors": [["#spinner"]], "visible": false},
	  {"type": "waitForExpression", "expression": "document.readyState === 'complete'", "timeout": 3000},
	  {"type": "doubleClick", "selectors": [["#row"]]}
	]}`

	result, err := (&importers.ChromeRecorderConverter{}).Convert([]byte(input))
	r.NoError(err)

	script, ok := result.Document.Checks[0].Config["script"].(string)
	r.True(ok)
	r.Contains(script, "function poll(")
	r.Contains(script, "els.length === 3")
	r.Contains(script, "getAttribute")
	r.Contains(script, `poll("step 4: waitForExpression", "document.readyState === 'complete'", 3000)`)
	r.Equal(2, countOccurrences(script, "page.click("), "a double click is two clicks")
	r.True(result.Document.Checks[0].Enabled)

	spec := &checkerdef.CheckSpec{Config: result.Document.Checks[0].Config}
	r.NoError(checkjsconfig.ValidateSpec(spec))
}

func countOccurrences(text, sub string) int {
	count := 0

	for i := 0; i+len(sub) <= len(text); i++ {
		if text[i:i+len(sub)] == sub {
			count++
		}
	}

	return count
}

func TestChromeRecorderConvertEndpoint(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	harness := newConvertHarness(t, "")
	body := readFixture(t, "chrome-recorder-login.json")

	result := runSourceRoundTrip(t, harness, "chrome-recorder", body)
	r.Equal(1, result.Converted)

	raw := harness.post(t, "chrome-recorder", true, body).Body.String()
	r.NotContains(raw, "s3cret-Passw0rd!", "the password never appears in the convert response")

	bad := harness.post(t, "chrome-recorder", false, []byte(`{"title":"x","steps":[]}`))
	r.Equal(http.StatusBadRequest, bad.Code)
	r.Contains(bad.Body.String(), "no steps")
}

func recorderScriptOf(t *testing.T, input string) (string, bool) {
	t.Helper()

	result, err := (&importers.ChromeRecorderConverter{}).Convert([]byte(input))
	require.NoError(t, err)

	script, ok := result.Document.Checks[0].Config["script"].(string)
	require.True(t, ok)

	return script, result.Document.Checks[0].Enabled
}

func TestChromeRecorderMalformedStepBecomesATodo(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	script, enabled := recorderScriptOf(t, `{"steps": [
	  {"type": "navigate", "url": "https://a.example"},
	  {"type": "click", "timeout": "soon", "selectors": [["#go"]]}
	]}`)
	r.False(enabled)
	r.Contains(script, "// TODO step 2")

	_, err := (&importers.ChromeRecorderConverter{}).Convert(
		[]byte(`{"steps": [{"type": "navigate", "url": "https://a.example"}, {"timeout": "soon"}]}`))
	r.ErrorIs(err, importers.ErrRecorderInvalid)
	r.ErrorContains(err, "step 2 has no type")
}

func TestChromeRecorderViewportAndTitleDefaults(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for name, size := range map[string]string{
		"huge":  `"width": 9000, "height": 800`,
		"tiny":  `"width": 1280, "height": 100`,
		"close": `"width": 1300, "height": 820`,
	} {
		result, err := (&importers.ChromeRecorderConverter{}).Convert([]byte(`{"title": "  ", "steps": [
		  {"type": "navigate", "url": "https://a.example"},
		  {"type": "setViewport", ` + size + `}
		]}`))
		r.NoError(err, name)
		r.Equal("Chrome recording", result.Document.Checks[0].Name, name)

		warned := len(result.Warnings) > 0
		r.Equal(name != "close", warned, name)
	}
}

func TestChromeRecorderTimeouts(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	script, _ := recorderScriptOf(t, `{"title": "t", "timeout": 7000, "steps": [
	  {"type": "navigate", "url": "https://a.example"},
	  {"type": "waitForElement", "selectors": [["#a"]]},
	  {"type": "waitForElement", "selectors": [["#b"]], "timeout": 99999}
	]}`)
	r.Contains(script, `page.waitFor(["#a"], { timeout: 7000 })`, "the flow timeout is the default")
	r.Contains(script, `page.waitFor(["#b"], { timeout: 30000 })`, "a step timeout is clamped to the script budget")
}

func TestChromeRecorderSelectorShapes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	longName := strings.Repeat("x", 120)

	script, enabled := recorderScriptOf(t, `{"title": "s", "steps": [
	  {"type": "navigate", "url": "https://a.example", "assertedEvents": [
	    {"type": "navigation", "url": "https://a.example/home"}, {"type": "navigation"}]},
	  {"type": "click", "selectors": ["#plain", 42, ["#a", "#shadow"], ["aria/Save[role=\"button\"]"]]},
	  {"type": "click", "selectors": [["aria/Open[role=\"button\"]"]]},
	  {"type": "click", "selectors": [["#`+longName+`"]]},
	  {"type": "click", "selectors": [["#a", "#shadow"]]},
	  {"type": "change", "selectors": [["#a", "#shadow"]], "value": "v"},
	  {"type": "waitForElement", "selectors": [["#a", "#shadow"]]},
	  {"type": "waitForElement", "selectors": [["aria/Save"]], "count": 2, "visible": false},
	  {"type": "waitForElement", "selectors": [["xpath//html/body"]], "count": 2, "operator": "<="},
	  {"type": "waitForElement", "selectors": [["pierce/#deep"]], "count": 1, "operator": "=="},
	  {"type": "waitForElement", "selectors": [["text/Hello"], ["#fallback"]], "count": 4, "operator": "!!"},
	  {"type": "waitForElement", "selectors": [["#ready"]], "attributes": {"a": "b"}}
	]}`)

	r.False(enabled, "unusable selectors pause the check")
	r.Contains(script, `waitUrl("https://a.example/home")`)
	r.Equal(1, countOccurrences(script, `waitUrl("`), "an event without a URL asserts nothing")
	r.Contains(script, "// TODO step 5")
	r.Contains(script, "// TODO step 6")
	r.Contains(script, "// TODO step 7")
	r.Contains(script, "document.evaluate(")
	r.Contains(script, "els.length <= 2")
	r.Contains(script, "els.length === 1")
	r.Contains(script, "els.length >= 4", "an unknown operator falls back to at-least")
	r.Contains(script, `step 3: click Open"`, "the aria prefix and role suffix are dropped from the label")
	r.NotContains(script, "click #"+longName, "labels are truncated")
}
