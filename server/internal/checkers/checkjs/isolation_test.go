package checkjs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// The test binary is the script child too (it links this package): children
// get __childPid(), __rlimitAS() (the RLIMIT_AS it runs under, 0 where the
// platform has none) and __nativeSpin(), a native call that never returns.
// Set by a variable, before the child's init runs.
var _ = func() bool {
	childExtraGlobals = func(r *jsRuntime) {
		_ = r.vm.Set("__childPid", func(goja.FunctionCall) goja.Value {
			return r.vm.ToValue(os.Getpid())
		})
		_ = r.vm.Set("__rlimitAS", func(goja.FunctionCall) goja.Value {
			return r.vm.ToValue(strconv.FormatUint(testRlimitAS(), 10))
		})
		_ = r.vm.Set("__nativeSpin", func(goja.FunctionCall) goja.Value {
			for {
				runtime.Gosched()
			}
		})
	}

	return true
}()

func runIsolated(t *testing.T, script string, timeout time.Duration) *checkerdef.Result {
	t.Helper()

	result, err := (&JSChecker{}).Execute(context.Background(), &JSConfig{Script: script, Timeout: timeout})
	require.NoError(t, err)

	return result
}

// The script runs in another process.
func TestIsolationRunsTheScriptInAChild(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	result := runIsolated(t, `return { status: "up", output: { pid: __childPid() } };`, 5*time.Second)

	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	r.NotEqual(int64(os.Getpid()), result.Output["pid"])
	r.NotZero(result.Output["pid"])
}

// Flattening a 256 Mi-character rope with s[0] is not charged to the budget:
// the child passes its memory limit and is killed, the run ends as an error
// naming the limit, and this process carries on.
func TestIsolationRopeFlattenHitsTheMemoryLimit(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	result := runIsolated(t, `var s = "x".repeat(1 << 20);
for (var i = 0; i < 8; i++) { s = s + s; }
return { status: s[0] === "x" ? "up" : "down" };`, 20*time.Second)

	r.Equal(checkerdef.StatusError, result.Status, result.Output)
	r.Contains(result.Output["error"], "memory limit exceeded")
	r.Contains(result.Output["error"], strconv.FormatInt(Isolation().MemoryLimit/bytesPerMiB, 10)+" MiB")

	// The parent is alive and runs the next script.
	next := runIsolated(t, `return { status: "up" };`, 5*time.Second)
	r.Equal(checkerdef.StatusUp, next.Status, next.Output)
}

// A native call that never returns cannot be interrupted: the child is
// killed at the timeout and the run is a timeout.
func TestIsolationKillsANativeCallAtTheTimeout(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	start := time.Now()
	result := runIsolated(t, `console.log("spinning"); __nativeSpin(); return { status: "up" };`, time.Second)

	r.Equal(checkerdef.StatusTimeout, result.Status, result.Output)
	r.Contains(result.Output["error"], "script timed out after 1s")
	r.Contains(result.Output["error"], "killed")
	r.Less(time.Since(start), time.Second+childKillGrace+3*time.Second)
}

// The bindings served by the parent work from the child: http, an
// http.session() handle, and console.
func TestIsolationBindingsWorkFromTheChild(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "abc"})
		_, _ = w.Write([]byte(`{"hello":"` + req.URL.Path + `"}`))
	}))
	t.Cleanup(srv.Close)

	script := `var r = http.get(env.BASE + "/one");
var s = http.session();
s.get(env.BASE + "/two");
var cookies = s.cookies(env.BASE);
console.log("pid", __childPid());
return { status: r.statusCode === 200 ? "up" : "down", output: {
  body: r.body, cookies: JSON.stringify(cookies) } };`

	result, err := (&JSChecker{}).Execute(context.Background(), &JSConfig{
		Script: script, Env: map[string]string{"BASE": srv.URL}, Timeout: 10 * time.Second,
	})
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	body, _ := result.Output["body"].(string)
	r.JSONEq(`{"hello":"/one"}`, body)
	r.Contains(result.Output["cookies"], "abc")
	r.Contains(result.Output["console"], "[log] pid ")
	r.NotContains(result.Output["console"], "[log] pid "+strconv.Itoa(os.Getpid())+"\n")
}

// What a parent binding throws reaches the script as the same error, which
// it can catch.
//
//nolint:paralleltest // Swaps the package-level TypeEnabled gate.
func TestIsolationBindingThrowReachesTheScript(t *testing.T) {
	installGate(t, checkerdef.CheckTypeTCP)

	r := require.New(t)
	result := runIsolated(t, `try { tcp.connect("127.0.0.1:1"); } catch (e) {
  return { status: "up", output: { caught: e.name + ": " + e.message } }; }
return { status: "down" };`, 5*time.Second)

	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	r.Contains(result.Output["caught"], "GoError: ")
	r.Contains(result.Output["caught"], "tcp")

	uncaught := runIsolated(t, `tcp.connect("127.0.0.1:1"); return { status: "up" };`, 5*time.Second)
	r.Equal(checkerdef.StatusError, uncaught.Status)
	r.Contains(uncaught.Output["error"], "script error: GoError: ")
}

// A sub-check runs in the parent, through the registry the parent holds.
//
//nolint:paralleltest // Swaps the package-level ResolveChecker.
func TestIsolationSubCheckRunsInTheParent(t *testing.T) {
	stub := &stubChecker{}
	installStubResolver(t, map[checkerdef.CheckType]*stubChecker{checkerdef.CheckTypeTCP: stub})

	r := require.New(t)
	result := runIsolated(t, `var c = solidping.tcp({ host: "127.0.0.1", port: 1 });
return { status: c.status, output: { stub: c.output.stub } };`, 5*time.Second)

	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	r.Equal(true, result.Output["stub"])
	r.Equal(int32(1), stub.calls.Load())
}

// A repair-held run that reaches a host outside its grant from the child is
// refused by the parent's allowlist.
func TestIsolationRepairHoldRefusesFromTheChild(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	acme, evil := newSecretSink(t), newSecretSink(t)
	cfg := repairedConfig(acmeScript(acme.port, `"http://localhost:`+evil.port+`"`), []string{"127.0.0.1"})

	result, err := (&JSChecker{}).Execute(context.Background(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusError, result.Status, result.Output)
	r.Equal(true, result.Output[OutputKeyRepairRefused])
	r.Zero(evil.hits())
	r.True(acme.sawSecret())
}

// On Linux the child runs under an address-space rlimit; the Go runtime
// still runs the script under it.
func TestIsolationChildRunsUnderAnRlimit(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("RLIMIT_AS is only set on Linux")
	}

	r := require.New(t)
	result := runIsolated(t, `var a = []; for (var i = 0; i < 1e5; i++) { a.push("v" + i); }
return { status: "up", output: { as: __rlimitAS() } };`, 10*time.Second)

	r.Equal(checkerdef.StatusUp, result.Status, result.Output)

	text, ok := result.Output["as"].(string)
	r.True(ok)

	limit, err := strconv.ParseUint(text, 10, 64)
	r.NoError(err)
	r.Positive(limit)
	r.Less(limit, uint64(1)<<62, "a finite limit")
}

// A JS plugin's config reaches the child as the `config` global, and an
// empty one still makes the script a plugin (warning is a valid status).
func TestIsolationPluginConfigReachesTheChild(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	result, err := (&JSChecker{}).Execute(context.Background(), &JSConfig{
		Script:       `return { status: "up", output: { n: config.n, frozen: Object.isFrozen(config) } };`,
		PluginConfig: map[string]any{"n": 3},
	})
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	r.Equal(int64(3), result.Output["n"])
	r.Equal(true, result.Output["frozen"])

	warning, err := (&JSChecker{}).Execute(context.Background(), &JSConfig{
		Script: `return { status: "warning" };`, PluginConfig: map[string]any{},
	})
	r.NoError(err)
	r.Equal(checkerdef.StatusWarning, warning.Status, warning.Output)
}

// A child that cannot start is an error result, not a crash.
//
//nolint:paralleltest // Swaps the package-level isolation settings.
func TestIsolationChildThatCannotStart(t *testing.T) {
	previous := SetIsolation(IsolationSettings{Enabled: true, Executable: "/nonexistent/solidping"})
	t.Cleanup(func() { SetIsolation(previous) })

	result := runIsolated(t, `return { status: "up" };`, time.Second)

	require.Equal(t, checkerdef.StatusError, result.Status)
	require.Contains(t, result.Output["error"], "could not start the script process")
}

// SP_JS_ISOLATION=off runs the script in this process.
//
//nolint:paralleltest // Swaps the package-level isolation settings.
func TestIsolationOffRunsInProcess(t *testing.T) {
	previous := SetIsolation(IsolationSettings{Enabled: false})
	t.Cleanup(func() { SetIsolation(previous) })

	result := runIsolated(t, `return { status: typeof __childPid === "undefined" ? "up" : "down" };`, time.Second)

	require.Equal(t, checkerdef.StatusUp, result.Status, result.Output)
}

func TestIsolationFromEnv(t *testing.T) {
	t.Parallel()

	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}

	r := require.New(t)

	def := isolationFromEnv(env(nil))
	r.True(def.Enabled)
	r.Equal(int64(defaultChildMemoryLimit), def.MemoryLimit)

	set := isolationFromEnv(env(map[string]string{
		"SP_JS_ISOLATION": "OFF", "SP_JS_MEMORY_LIMIT_MB": "512", "SP_JS_CGROUP": "/sys/fs/cgroup/sp",
	}))
	r.False(set.Enabled)
	r.Equal(int64(512<<20), set.MemoryLimit)
	r.Equal("/sys/fs/cgroup/sp", set.CgroupDir)

	tooSmall := isolationFromEnv(env(map[string]string{"SP_JS_MEMORY_LIMIT_MB": "1"}))
	r.Equal(int64(defaultChildMemoryLimit), tooSmall.MemoryLimit)
}

func TestLastLine(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal("fatal error: runtime: out of memory", lastLine("\nfatal error: runtime: out of memory\n\ngoroutine 1\n"))
	r.Equal("b", lastLine("a\nb\n"))
	r.Empty(strings.TrimSpace(lastLine("")))
}
