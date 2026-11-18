package importers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsPingdomTMS(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		`not json`:                        false,
		`{"steps": [{"fn": "go_to"}]}`:    true,
		`{"steps": null, "type": "x"}`:    false,
		`{"type": "script"}`:              true,
		`{"type": "Recording"}`:           true,
		`{"type": 12}`:                    false,
		`{"type": "http", "url": "a.io"}`: false,
		`{}`:                              false,
	}

	for input, want := range cases {
		require.Equal(t, want, isPingdomTMS(json.RawMessage(input)), input)
	}
}

func TestPingdomTMSTagNames(t *testing.T) {
	t.Parallel()

	tags := pingdomTMSTagNames([]json.RawMessage{
		json.RawMessage(`"prod"`), json.RawMessage(`{"name": "web"}`), json.RawMessage(`42`),
	})

	require.Len(t, tags, 2)
	require.Equal(t, "prod", tags[0].Name)
	require.Equal(t, "web", tags[1].Name)
}

// tmsFakeServer answers the transaction endpoints with the given handlers.
func tmsFakeServer(t *testing.T, list, detail http.HandlerFunc) *PingdomConverter {
	t.Helper()

	if detail == nil {
		detail = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/3.1/tms/check", list)
	mux.HandleFunc("/api/3.1/tms/check/", detail)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return NewPingdomConverter(PingdomOptions{BaseURL: server.URL, TMSPageSize: 2})
}

func TestFetchTMSFailureModes(t *testing.T) {
	t.Parallel()

	okList := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"checks":[{"id":7}]}`)) }

	t.Run("an unreadable list is skipped with a warning", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		conv := tmsFakeServer(t,
			func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html>`)) }, nil)
		warn := &warnings{}

		items, err := conv.fetchTMS(context.Background(), "tok", warn)
		r.NoError(err)
		r.Empty(items)
		r.Contains(warn.result()[0].Message, "unreadable")
	})

	t.Run("a failing detail read aborts", func(t *testing.T) {
		t.Parallel()

		conv := tmsFakeServer(t, okList,
			func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })

		_, err := conv.fetchTMS(context.Background(), "tok", &warnings{})
		require.ErrorIs(t, err, ErrPingdomUnauthorized)
	})

	t.Run("an unexpected list failure is a warning", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		conv := tmsFakeServer(t,
			func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }, nil)
		warn := &warnings{}

		items, err := conv.fetchTMS(context.Background(), "tok", warn)
		r.NoError(err)
		r.Empty(items)
		r.Contains(warn.result()[0].Message, "could not be listed")
	})

	t.Run("an account without the product yields nothing", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		conv := tmsFakeServer(t,
			func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }, nil)
		warn := &warnings{}

		items, err := conv.fetchTMS(context.Background(), "tok", warn)
		r.NoError(err)
		r.Empty(items)
		r.Empty(warn.result())
	})
}

func TestConvertPingdomTMSEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("undecodable", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		warn := &warnings{}
		_, ok := convertPingdomTMS(json.RawMessage(`{"steps": 3}`), newSlugSet(), warn)
		r.False(ok)
		r.Contains(warn.result()[0].Message, "could not be decoded")
	})

	t.Run("no steps", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		warn := &warnings{}
		_, ok := convertPingdomTMS(json.RawMessage(`{"id": 5, "type": "script"}`), newSlugSet(), warn)
		r.False(ok)
		r.Equal("pingdom-transaction-5", warn.result()[0].Item, "an unnamed check is named after its id")
		r.Contains(warn.result()[0].Message, "no steps found")
	})

	t.Run("region, tags, paused and confirmation", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		warn := &warnings{}
		check, ok := convertPingdomTMS(json.RawMessage(`{
		  "id": 5, "name": "Login", "active": false, "interval": 5, "region": "eu",
		  "send_notification_when_down": 2, "tags": ["prod"],
		  "steps": [{"fn": "go_to", "args": {"url": "acme.example"}}]
		}`), newSlugSet(), warn)
		r.True(ok)
		r.False(check.Enabled)
		r.NotNil(check.ConfirmationPeriodSeconds)
		r.Equal(2*5*60, *check.ConfirmationPeriodSeconds)
		r.NotEmpty(check.Labels)

		found := false

		for _, w := range warn.result() {
			found = found || strings.Contains(w.Message, "no SolidPing counterpart")
		}

		r.True(found)
	})
}

func TestPingdomBasicAuthSources(t *testing.T) {
	t.Parallel()

	newScript := func() *pingdomScript {
		return &pingdomScript{name: "acme", warn: &warnings{}, helpers: map[string]bool{}, secrets: map[string]string{}}
	}

	t.Run("credentials without a go_to are reported", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		script := newScript()
		script.generate(&pingdomTMSCheck{Steps: []pingdomTMSStep{
			{Fn: "basic_auth", Args: map[string]any{"username": "alice", "password": "pw"}},
			{Fn: "sleep", Args: map[string]any{"seconds": 1000.0}},
		}})

		messages := make([]string, 0, len(script.warn.result()))

		for _, w := range script.warn.result() {
			messages = append(messages, w.Message)
		}

		r.Contains(strings.Join(messages, "\n"), "has no go_to step to arm them")
		r.Contains(strings.Join(messages, "\n"), "clamped")
		r.NotContains(script.String(), "alice")
	})

	t.Run("metadata authentications, only the first is kept", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		var check pingdomTMSCheck

		r.NoError(json.Unmarshal([]byte(`{
		  "metadata": {"authentications": {"httpAuthentications": [
		    {"host": "a", "credentials": {"userName": "u1", "password": "p1"}},
		    {"host": "b", "credentials": {"userName": "u2", "password": "p2"}}
		  ]}},
		  "steps": [{"fn": "go_to", "args": {"url": "https://acme.example"}}]
		}`), &check))

		script := newScript()
		script.generate(&check)

		r.Equal("u1", script.secrets[pingdomSecretUser])
		r.Contains(script.String(), "page.basicAuth(secrets.")
		r.Len(script.warn.result(), 1)
		r.Contains(script.warn.result()[0].Message, "only the first was imported")
	})

	t.Run("empty credentials arm nothing", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		script := newScript()
		script.generate(&pingdomTMSCheck{Steps: []pingdomTMSStep{
			{Fn: "basic_auth"},
			{Fn: "go_to", Args: map[string]any{"url": "https://acme.example"}},
		}})

		r.Empty(script.secrets)
		r.NotContains(script.String(), "basicAuth")
	})
}

func TestPingdomUnusableURLs(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, ok := normalizeURL("   ")
	r.False(ok)

	script := &pingdomScript{name: "acme", warn: &warnings{}, helpers: map[string]bool{}, secrets: map[string]string{}}
	script.generate(&pingdomTMSCheck{Steps: []pingdomTMSStep{
		{Fn: "go_to", Args: map[string]any{"url": "https://acme.example"}},
		{Fn: "url", Args: map[string]any{"url": "ftp://acme.example"}},
	}})

	r.Contains(script.String(), "// TODO step 2")
	r.True(script.untranslatable)
}

func TestPingdomTMSFillCredentialGoesToSecrets(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	warn := &warnings{}
	check, ok := convertPingdomTMS(json.RawMessage(`{
	  "id": 7, "name": "Acme login", "type": "script",
	  "steps": [
	    {"fn": "go_to", "args": {"url": "acme.example/login"}},
	    {"fn": "fill", "args": {"input": "Email", "value": "ops@acme.example"}},
	    {"fn": "wpm_fill", "args": {"input": "Password", "value": "hunter2-acme"}},
	    {"fn": "fill", "args": {"input": "#search", "value": "Bearer abc"}},
	    {"fn": "click", "args": {"element": "Sign in"}}
	  ]
	}`), newSlugSet(), warn)
	r.True(ok)

	script, isString := check.Config["script"].(string)
	r.True(isString)
	r.NotContains(script, "hunter2-acme")
	r.NotContains(script, "Bearer abc")
	r.Contains(script, `page.fill("auto=Password", secrets.PINGDOM_FILL_1)`)
	r.Contains(script, `page.fill("auto=#search", secrets.PINGDOM_FILL_2)`)
	r.Contains(script, `"ops@acme.example"`, "a non-credential value stays a literal")

	secrets, isMap := check.Config["secrets"].(map[string]any)
	r.True(isMap)
	r.Equal("hunter2-acme", secrets["PINGDOM_FILL_1"])
	r.Equal("Bearer abc", secrets["PINGDOM_FILL_2"])
	r.NotContains(secrets, pingdomSecretUser, "no basic auth was configured")

	var moved, stayed bool

	for _, w := range warn.result() {
		r.NotContains(w.Message, "hunter2-acme")
		r.NotContains(w.Message, "go_to step to arm them", "fill secrets are not basic auth")

		moved = moved || strings.Contains(w.Message, "2 typed value(s) were moved")
		stayed = stayed || strings.Contains(w.Message, "1 typed value(s) stayed in the script")
	}

	r.True(moved)
	r.True(stayed)
}

func TestTypedSecretRule(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		value, field string
		want         bool
	}{
		{"x", "Password", true},
		{"x", "#user_pin", true},
		{"x", "otp-code", true},
		{"x", "api_key", true},
		{"x", "Mot de passe", true},
		{"x", "Passwort", true},
		{"x", "Contraseña", true},
		{"x", "client_secret", true},
		{"x", "input[type=password]", true},
		{"Bearer abc", "#search", true},
		{"sk9Fq2LxP7vRz4TbW1mN8cYd", "#field", true},
		{"hello", "#search", false},
		{"ops@acme.example", "Email", false},
		{"Paris", "#shipping-city", false},
		{"https://acme.example/a1b2c3d4e5f6g7h8", "#website", false},
		{"aaaaaaaaaaaaaaaaaaaaaa11", "#field", false},
	} {
		require.Equalf(t, tc.want, isTypedSecret(tc.value, tc.field), "value %q field %q", tc.value, tc.field)
	}
}
