package plugins

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
	"github.com/fclairamb/solidping/server/internal/checkers/registrycore"
	"github.com/fclairamb/solidping/server/internal/egress"
)

const gopherType = checkerdef.CheckType("x-gopher")

// repoPluginsDir is the repo's plugins/ folder, the one make dev loads.
const repoPluginsDir = "../../../plugins"

//nolint:gochecknoglobals // the gopher type is registered once per test binary
var (
	gopherOnce sync.Once
	gopherSet  *Set
)

// loadGopher registers the reference plugin from plugins/gopher, once.
func loadGopher(t *testing.T) {
	t.Helper()

	gopherOnce.Do(func() {
		gopherSet = Load(t.Context(), repoPluginsDir, nil)
	})

	require.Empty(t, gopherSet.Errors)
	require.Contains(t, gopherSet.Types, gopherType)
}

// fakeGopher serves one canned exchange per connection on 127.0.0.1.
func fakeGopher(t *testing.T, handle func(conn net.Conn, selector string)) (string, int) {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			go func() {
				defer func() { _ = conn.Close() }()

				buf := make([]byte, 1024)
				n, _ := conn.Read(buf)
				handle(conn, strings.TrimSuffix(string(buf[:n]), "\r\n"))
			}()
		}
	}()

	addr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)

	return "127.0.0.1", addr.Port
}

func runGopher(ctx context.Context, t *testing.T, config map[string]any) *checkerdef.Result {
	t.Helper()

	checker, ok := registrycore.GetChecker(gopherType)
	require.True(t, ok)

	cfg, ok := registrycore.ParseConfig(gopherType)
	require.True(t, ok)
	require.NoError(t, cfg.FromMap(config))

	result, err := checker.Execute(ctx, cfg)
	require.NoError(t, err)

	return result
}

// selfHosted is a context with the self-hosted (permissive) egress policy.
func selfHosted(t *testing.T) context.Context {
	t.Helper()

	return egress.WithGuard(t.Context(), egress.New(true))
}

const gopherMenu = "iWelcome to acme\t\terror.host\t1\r\n" +
	"1Docs\t/docs\tgopher.acme.com\t70\r\n" +
	"0About\t/about.txt\tgopher.acme.com\t70\r\n" +
	".\r\n"

func TestGopherMenuIsUp(t *testing.T) {
	t.Parallel()
	loadGopher(t)

	r := require.New(t)

	var gotSelector string

	var mu sync.Mutex

	host, port := fakeGopher(t, func(conn net.Conn, selector string) {
		mu.Lock()
		gotSelector = selector
		mu.Unlock()

		_, _ = io.WriteString(conn, gopherMenu)
		time.Sleep(time.Second) // keep the connection open: the "." ends the menu
	})

	res := runGopher(selfHosted(t), t, map[string]any{"host": host, "port": port, "selector": "/docs"})
	r.Equal(checkerdef.StatusUp, res.Status, res.Output)
	r.InDelta(3, res.Metrics["items"], 0)
	r.Positive(res.Metrics["bytes"])
	r.Equal("iWelcome to acme\t\terror.host\t1", res.Output["message"])

	mu.Lock()
	r.Equal("/docs", gotSelector)
	mu.Unlock()
}

func TestGopherTextDocumentEndedByClose(t *testing.T) {
	t.Parallel()
	loadGopher(t)

	host, port := fakeGopher(t, func(conn net.Conn, _ string) {
		_, _ = io.WriteString(conn, "Hello from acme\r\nThis is a plain text file.\r\n")
	})

	res := runGopher(selfHosted(t), t, map[string]any{"host": host, "port": port})
	require.Equal(t, checkerdef.StatusUp, res.Status, res.Output)
	require.InDelta(t, 0, res.Metrics["items"], 0)
	require.Equal(t, "Hello from acme", res.Output["message"])
}

func TestGopherErrorItemIsDown(t *testing.T) {
	t.Parallel()
	loadGopher(t)

	host, port := fakeGopher(t, func(conn net.Conn, _ string) {
		_, _ = io.WriteString(conn, "3'/nope' does not exist\t\terror.host\t1\r\n.\r\n")
	})

	res := runGopher(selfHosted(t), t, map[string]any{"host": host, "port": port, "selector": "/nope"})
	require.Equal(t, checkerdef.StatusDown, res.Status, res.Output)
}

func TestGopherContains(t *testing.T) {
	t.Parallel()
	loadGopher(t)

	host, port := fakeGopher(t, func(conn net.Conn, _ string) {
		_, _ = io.WriteString(conn, gopherMenu)
	})

	present := runGopher(selfHosted(t), t, map[string]any{"host": host, "port": port, "contains": "About"})
	require.Equal(t, checkerdef.StatusUp, present.Status, present.Output)

	absent := runGopher(selfHosted(t), t, map[string]any{"host": host, "port": port, "contains": "Missing"})
	require.Equal(t, checkerdef.StatusDown, absent.Status, absent.Output)
	require.Contains(t, fmt.Sprint(absent.Output["error"]), "does not contain")
}

func TestGopherEmptyResponseIsDown(t *testing.T) {
	t.Parallel()
	loadGopher(t)

	host, port := fakeGopher(t, func(_ net.Conn, _ string) {})

	res := runGopher(selfHosted(t), t, map[string]any{"host": host, "port": port})
	require.Equal(t, checkerdef.StatusDown, res.Status, res.Output)
	require.Equal(t, "empty response", res.Output["message"])
}

func TestGopherSilentServerTimesOut(t *testing.T) {
	t.Parallel()
	loadGopher(t)

	host, port := fakeGopher(t, func(_ net.Conn, _ string) {
		time.Sleep(5 * time.Second)
	})

	res := runGopher(selfHosted(t), t, map[string]any{"host": host, "port": port, "timeout": "500ms"})
	require.Equal(t, checkerdef.StatusTimeout, res.Status, res.Output)
}

func TestGopherClosedPortIsDown(t *testing.T) {
	t.Parallel()
	loadGopher(t)

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)
	require.NoError(t, listener.Close())

	res := runGopher(selfHosted(t), t, map[string]any{"host": "127.0.0.1", "port": addr.Port})
	require.Equal(t, checkerdef.StatusDown, res.Status, res.Output)
}

func TestGopherSchemaValidation(t *testing.T) {
	t.Parallel()
	loadGopher(t)

	cases := map[string]struct {
		config map[string]any
		field  string
	}{
		"missing host": {map[string]any{"port": 70}, "host"},
		"port 0":       {map[string]any{"host": "gopher.acme.com", "port": 0}, "port"},
		"port 70000":   {map[string]any{"host": "gopher.acme.com", "port": 70000}, "port"},
		"unknown key":  {map[string]any{"host": "gopher.acme.com", "command": "rm"}, "config"},
		"port string":  {map[string]any{"host": "gopher.acme.com", "port": "70"}, "port"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := configregistry.ValidateSpec(gopherType, &checkerdef.CheckSpec{Config: tc.config})
			require.Error(t, err)

			configErr := checkerdef.IsConfigError(err)
			require.NotNil(t, configErr, err)
			require.Equal(t, tc.field, configErr.Parameter, err)
		})
	}

	spec := &checkerdef.CheckSpec{Config: map[string]any{"host": "gopher.acme.com", "port": 70}}
	require.NoError(t, configregistry.ValidateSpec(gopherType, spec))
	require.Equal(t, "x-gopher-gopher-acme-com", spec.Slug)
}

// Under the SaaS (enforcing) egress policy a loopback target is refused; the
// same target passes in self-hosted mode.
func TestGopherEgressGuard(t *testing.T) {
	t.Parallel()
	loadGopher(t)

	host, port := fakeGopher(t, func(conn net.Conn, _ string) {
		_, _ = io.WriteString(conn, gopherMenu)
	})

	config := map[string]any{"host": host, "port": port}

	denied := runGopher(egress.WithGuard(t.Context(), egress.New(false)), t, config)
	require.Equal(t, checkerdef.StatusDown, denied.Status, denied.Output)
	require.Contains(t, fmt.Sprint(denied.Output["error"]), "denied by egress policy")

	allowed := runGopher(selfHosted(t), t, config)
	require.Equal(t, checkerdef.StatusUp, allowed.Status, allowed.Output)
}

func TestGopherIPv6HostIsBracketed(t *testing.T) {
	t.Parallel()
	loadGopher(t)

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback")
	}

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}

		defer func() { _ = conn.Close() }()

		_, _ = conn.Read(make([]byte, 64))
		_, _ = io.WriteString(conn, gopherMenu)
	}()

	_, portStr, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portStr)

	res := runGopher(selfHosted(t), t, map[string]any{"host": "::1", "port": port})
	require.Equal(t, checkerdef.StatusUp, res.Status, res.Output)
}
