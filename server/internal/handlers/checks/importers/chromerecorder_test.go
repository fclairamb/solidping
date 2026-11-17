package importers_test

import (
	"net/http"
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
			r.Contains(warning.Message, "stay in clear")
		}
	}

	r.True(found, "a secrets warning is expected")
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
