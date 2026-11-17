package importers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/dop251/goja"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	jsconfig "github.com/fclairamb/solidping/server/internal/checkers/checkjs/config"
	"github.com/fclairamb/solidping/server/internal/handlers/checks/importers"
)

const pingdomTestToken = "pd_super_secret_token_value"

// pingdomServer is a fake Pingdom API 3.1 serving the fixtures.
type pingdomServer struct {
	*httptest.Server
	mu    sync.Mutex
	auths []string
	paths []string
}

func newPingdomServer(t *testing.T, status int) *pingdomServer {
	t.Helper()

	srv := &pingdomServer{}

	var details []struct {
		Check json.RawMessage `json:"check"`
	}

	require.NoError(t, json.Unmarshal(readFixture(t, "pingdom-check-details.json"), &details))

	byID := map[string]json.RawMessage{}

	for _, d := range details {
		var c struct {
			ID int `json:"id"`
		}

		require.NoError(t, json.Unmarshal(d.Check, &c))

		byID[strconv.Itoa(c.ID)] = d.Check
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/3.1/checks", func(w http.ResponseWriter, r *http.Request) {
		srv.record(r)

		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"echo ` + r.Header.Get("Authorization") + `"}`))

			return
		}

		_, _ = w.Write(readFixture(t, "pingdom-checks.json"))
	})
	mux.HandleFunc("/api/3.1/checks/", func(w http.ResponseWriter, r *http.Request) {
		srv.record(r)

		check, ok := byID[strings.TrimPrefix(r.URL.Path, "/api/3.1/checks/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		_, _ = w.Write([]byte(`{"check":` + string(check) + `}`))
	})

	srv.Server = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func (s *pingdomServer) record(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.auths = append(s.auths, r.Header.Get("Authorization"))
	s.paths = append(s.paths, r.URL.RequestURI())
}

func pingdomTokenBody(t *testing.T) []byte {
	t.Helper()

	body, err := json.Marshal(map[string]string{"token": pingdomTestToken})
	require.NoError(t, err)

	return body
}

func TestPingdomConverterGoldenFromPastedDetails(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	conv := importers.NewPingdomConverter(importers.PingdomOptions{})
	r.Equal("pingdom", conv.Source())

	result, err := conv.Convert(readFixture(t, "pingdom-check-details.json"))
	r.NoError(err)
	r.Len(result.Document.Checks, 10, "the transaction check is skipped")

	assertGolden(t, "pingdom", result)
}

func TestPingdomConverterPastedListUsesListFields(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	conv := importers.NewPingdomConverter(importers.PingdomOptions{})
	result, err := conv.Convert(readFixture(t, "pingdom-checks.json"))
	r.NoError(err)
	r.Len(result.Document.Checks, 10)
}

func TestPingdomConverterFetchSendsBearerAndMatchesPaste(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newPingdomServer(t, 0)
	conv := importers.NewPingdomConverter(importers.PingdomOptions{BaseURL: srv.URL})

	fetched, err := conv.Convert(pingdomTokenBody(t))
	r.NoError(err)

	pasted, err := conv.Convert(readFixture(t, "pingdom-check-details.json"))
	r.NoError(err)

	fetchedJSON, err := json.Marshal(fetched.Document.Checks)
	r.NoError(err)

	pastedJSON, err := json.Marshal(pasted.Document.Checks)
	r.NoError(err)

	r.JSONEq(string(pastedJSON), string(fetchedJSON))
	r.Contains(srv.paths[0], "include_tags=true")
	r.Len(srv.paths, 12, "one list call plus one details call per check")

	for _, auth := range srv.auths {
		r.Equal("Bearer "+pingdomTestToken, auth)
	}

	r.NotContains(string(fetchedJSON), pingdomTestToken)

	warnings, err := json.Marshal(fetched.Warnings)
	r.NoError(err)
	r.NotContains(string(warnings), pingdomTestToken)
}

func TestPingdomConverterUnauthorizedAndRateLimit(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newPingdomServer(t, http.StatusUnauthorized)
	conv := importers.NewPingdomConverter(importers.PingdomOptions{BaseURL: srv.URL})

	_, err := conv.Convert(pingdomTokenBody(t))
	r.ErrorIs(err, importers.ErrPingdomUnauthorized)
	r.NotContains(err.Error(), pingdomTestToken)

	limited := newPingdomServer(t, http.StatusTooManyRequests)
	conv = importers.NewPingdomConverter(importers.PingdomOptions{BaseURL: limited.URL})

	_, err = conv.Convert(pingdomTokenBody(t))
	r.ErrorIs(err, importers.ErrPingdomRateLimited)

	failing := newPingdomServer(t, http.StatusInternalServerError)
	conv = importers.NewPingdomConverter(importers.PingdomOptions{BaseURL: failing.URL})

	_, err = conv.Convert(pingdomTokenBody(t))
	r.ErrorIs(err, importers.ErrPingdomAPI)
}

func TestPingdomConverterRequiresTokenOrPayload(t *testing.T) {
	t.Parallel()

	conv := importers.NewPingdomConverter(importers.PingdomOptions{})
	_, err := conv.Convert([]byte(`{"token":""}`))
	require.ErrorIs(t, err, importers.ErrPingdomTokenRequired)
}

func TestConvertEndpointPingdomIsIdempotentAndManaged(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newPingdomServer(t, 0)
	harness := newConvertHarnessWithPingdom(t, "", srv.URL)

	// runSourceRoundTrip covers dry-run, apply and the idempotent re-apply.
	result := runSourceRoundTrip(t, harness, "pingdom", pingdomTokenBody(t))
	r.Equal("pingdom", result.Manifest)
	r.Equal(10, result.Created)
}

func TestConvertEndpointPingdomBadTokenIsAValidationError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newPingdomServer(t, http.StatusUnauthorized)
	harness := newConvertHarnessWithPingdom(t, "", srv.URL)

	rec := harness.post(t, "pingdom", true, pingdomTokenBody(t))
	r.Equal(http.StatusBadRequest, rec.Code)
	r.Contains(rec.Body.String(), "VALIDATION_ERROR")
	r.NotContains(rec.Body.String(), pingdomTestToken)
	r.Equal(0, harness.countChecks(t))
}

func TestPingdomConverterSMTPEncryptionOffImplicitPortUsesStartTLS(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := `[{"check":{"id":1,"name":"Acme SMTP","hostname":"mail.acme.example","resolution":5,` +
		`"type":{"smtp":{"port":587,"encryption":true}}}}]`

	conv := importers.NewPingdomConverter(importers.PingdomOptions{})
	result, err := conv.Convert([]byte(payload))
	r.NoError(err)
	r.Len(result.Document.Checks, 1)

	cfg := result.Document.Checks[0].Config
	r.Equal(true, cfg["starttls"])
	r.NotContains(cfg, "tls")
}

// pingdomTMSServer fakes the Pingdom TMS endpoints, paginating the list.
func pingdomTMSServer(t *testing.T, listStatus int) (*httptest.Server, *pingdomServer) {
	t.Helper()

	srv := newPingdomServer(t, 0)
	details := map[string][]byte{
		"101": readFixture(t, "pingdom-tms-script.json"),
		"102": readFixture(t, "pingdom-tms-recording.json"),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/3.1/checks", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"checks":[]}`))
	})
	mux.HandleFunc("/api/3.1/tms/check", func(w http.ResponseWriter, r *http.Request) {
		srv.record(r)

		if listStatus != 0 {
			w.WriteHeader(listStatus)

			return
		}

		ids := []string{"101", "102"}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

		var page []string

		for i := offset; i < offset+limit && i < len(ids); i++ {
			page = append(page, `{"id":`+ids[i]+`,"name":"x","type":"script"}`)
		}

		_, _ = w.Write([]byte(`{"checks":[` + strings.Join(page, ",") + `]}`))
	})
	mux.HandleFunc("/api/3.1/tms/check/", func(w http.ResponseWriter, r *http.Request) {
		srv.record(r)

		body, ok := details[strings.TrimPrefix(r.URL.Path, "/api/3.1/tms/check/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		_, _ = w.Write(body)
	})

	tms := httptest.NewServer(mux)
	t.Cleanup(tms.Close)

	return tms, srv
}

func TestPingdomTMSFetchPaginatesAndSendsBearer(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	tms, rec := pingdomTMSServer(t, 0)
	conv := importers.NewPingdomConverter(importers.PingdomOptions{BaseURL: tms.URL, TMSPageSize: 1})

	result, err := conv.Convert(pingdomTokenBody(t))
	r.NoError(err)
	r.Len(result.Document.Checks, 2)
	r.Equal("js", result.Document.Checks[0].Type)

	rec.mu.Lock()
	defer rec.mu.Unlock()

	r.Contains(rec.paths, "/api/3.1/tms/check?limit=1&offset=0")
	r.Contains(rec.paths, "/api/3.1/tms/check?limit=1&offset=1")
	r.Contains(rec.paths, "/api/3.1/tms/check?limit=1&offset=2")
	r.Contains(rec.paths, "/api/3.1/tms/check/101")
	r.Contains(rec.paths, "/api/3.1/tms/check/102")

	for _, auth := range rec.auths {
		r.Equal("Bearer "+pingdomTestToken, auth)
	}
}

func TestPingdomTMSUnauthorizedIsAClearError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	tms, _ := pingdomTMSServer(t, http.StatusUnauthorized)
	conv := importers.NewPingdomConverter(importers.PingdomOptions{BaseURL: tms.URL})

	_, err := conv.Convert(pingdomTokenBody(t))
	r.ErrorIs(err, importers.ErrPingdomUnauthorized)
	r.NotContains(err.Error(), pingdomTestToken)
}

func TestPingdomTMSGoldenScript(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	conv := importers.NewPingdomConverter(importers.PingdomOptions{})
	result, err := conv.Convert(readFixture(t, "pingdom-tms-script.json"))
	r.NoError(err)
	assertGolden(t, "pingdom-tms-script", result)

	check := result.Document.Checks[0]
	r.True(check.Enabled)
	r.Equal("10m", check.Period)
	r.Equal("true", check.Labels["tms"])
}

func TestPingdomTMSGoldenRecordingWithUntranslatableStep(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	conv := importers.NewPingdomConverter(importers.PingdomOptions{})
	result, err := conv.Convert(readFixture(t, "pingdom-tms-recording.json"))
	r.NoError(err)
	assertGolden(t, "pingdom-tms-recording", result)

	check := result.Document.Checks[0]
	r.False(check.Enabled, "an untranslatable flow is created paused")
	r.Contains(check.Config["script"], "// TODO step 8")
	r.NotEmpty(result.Warnings)
}

func TestPingdomTMSUnknownFnAndHostileValuesStayInert(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	conv := importers.NewPingdomConverter(importers.PingdomOptions{})
	result, err := conv.Convert(readFixture(t, "pingdom-tms-unknown.json"))
	r.NoError(err)

	check := result.Document.Checks[0]
	r.False(check.Enabled)

	script, _ := check.Config["script"].(string)
	for _, line := range strings.Split(script, "\n") {
		r.NotContains(line, "evil();\n")
		if strings.Contains(line, "evil()") {
			r.True(strings.HasPrefix(line, "//"), "hostile text must stay inside a comment: %q", line)
			r.NotContains(line, "\r")
		}
	}

	r.GreaterOrEqual(len(result.Warnings), 2)
}

func TestPingdomTMSBasicAuthGoesToSecretsNotScriptNorManifest(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	conv := importers.NewPingdomConverter(importers.PingdomOptions{})

	for _, fixture := range []string{"pingdom-tms-script.json", "pingdom-tms-recording.json"} {
		result, err := conv.Convert(readFixture(t, fixture))
		r.NoError(err)

		check := result.Document.Checks[0]
		secrets, ok := check.Config["secrets"].(map[string]any)
		r.True(ok, fixture)
		r.NotEmpty(secrets["PINGDOM_BASIC_USER"])
		r.NotEmpty(secrets["PINGDOM_BASIC_PASSWORD"])

		script, _ := check.Config["script"].(string)
		for _, secret := range secrets {
			r.NotContains(script, secret)
		}

		r.Contains(script, "page.basicAuth(secrets.PINGDOM_BASIC_USER, secrets.PINGDOM_BASIC_PASSWORD)")
		r.Less(strings.Index(script, "page.basicAuth"), strings.Index(script, "page.goto"),
			"basicAuth is armed before the first navigation")
	}
}

func TestPingdomTMSGeneratedScriptPassesJSConfigValidation(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	conv := importers.NewPingdomConverter(importers.PingdomOptions{})

	for _, fixture := range []string{"pingdom-tms-script.json", "pingdom-tms-recording.json", "pingdom-tms-unknown.json"} {
		result, err := conv.Convert(readFixture(t, fixture))
		r.NoError(err)

		spec := &checkerdef.CheckSpec{Config: result.Document.Checks[0].Config}
		r.NoError(jsconfig.ValidateSpec(spec), fixture)

		script, _ := result.Document.Checks[0].Config["script"].(string)
		_, compileErr := goja.Compile("tms.js", "(function(){"+script+"\n})", false)
		r.NoError(compileErr, fixture)
	}
}

func TestPingdomTMSListWithoutStepsIsSkippedWithWarning(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	conv := importers.NewPingdomConverter(importers.PingdomOptions{})
	_, err := conv.Convert([]byte(`{"checks":[{"id":1,"name":"t","type":"script"}]}`))
	r.ErrorIs(err, importers.ErrEmptyInput)
}

func TestConvertEndpointPingdomTMSAppliesAndKeepsSecretsOutOfTheResponse(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	harness := newConvertHarnessWithPingdom(t, "", "")
	body := readFixture(t, "pingdom-tms-script.json")

	rec := harness.post(t, "pingdom", false, body)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	r.NotContains(rec.Body.String(), "s3cr3t-pw", "the basic auth password must not be echoed")
	r.NotContains(rec.Body.String(), "stagingUser")

	result := decodeConvert(t, rec)
	r.Empty(result.Errors)
	r.Equal(1, result.Created)
	r.NotContains(result.Manifest, "s3cr3t-pw")
	r.Equal(1, harness.countChecks(t))
}
