package registry

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/egress"
)

// runRegistered parses cfgMap for checkType through the registry, executes it
// with the given guard on the context, and returns the refusal the execution
// recorded (the contract the worker builds its result on) plus the result's
// text for diagnostics.
func runRegistered(
	t *testing.T, guard *egress.Guard, checkType checkerdef.CheckType, cfgMap map[string]any,
) (*egress.DeniedError, string) {
	t.Helper()

	checker, ok := GetChecker(checkType)
	require.True(t, ok)

	cfg, ok := ParseConfig(checkType)
	require.True(t, ok)
	require.NoError(t, cfg.FromMap(cfgMap))

	ctx, rec := egress.WithRecorder(egress.WithGuard(t.Context(), guard))

	result, err := checker.Execute(ctx, cfg)

	text := fmt.Sprint(err)
	if result != nil {
		text = fmt.Sprintf("%s %v", result.Status, result.Output)
	}

	return rec.Denied(), text
}

// Every loopback-reachable check type refuses its target under an enforcing
// policy, through its REAL registered checker and config parsing — the
// registry-level proof that the guard reaches each family's dial, not just
// the HTTP transport. The permissive run of the same config is the positive
// control: whatever it reports, it is never the egress refusal.
func TestCheckTypesRefuseLoopbackTargetsUnderAnEnforcingPolicy(t *testing.T) {
	t.Parallel()

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(httpServer.Close)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}

			_ = conn.Close()
		}
	}()

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)

	cases := map[checkerdef.CheckType]map[string]any{
		checkerdef.CheckTypeHTTP:         {"url": httpServer.URL},
		checkerdef.CheckTypePrometheus:   {"url": httpServer.URL + "/metrics"},
		checkerdef.CheckTypeVendorStatus: {"url": httpServer.URL},
		checkerdef.CheckTypeTCP:          {"host": host, "port": port},
		checkerdef.CheckTypeSSL:          {"host": host, "port": port},
		checkerdef.CheckTypeSSH:          {"host": host, "port": port},
		checkerdef.CheckTypeSMTP:         {"host": host, "port": port},
		checkerdef.CheckTypeRedis:        {"host": host, "port": port},
		checkerdef.CheckTypePostgreSQL:   {"host": host, "port": port, "username": "u", "database": "d"},
		checkerdef.CheckTypeMySQL:        {"host": host, "port": port, "username": "u"},
		checkerdef.CheckTypeMSSQL:        {"host": host, "port": port, "username": "u", "password": "p"},
		checkerdef.CheckTypeOracle:       {"host": host, "port": port, "username": "u", "password": "p", "serviceName": "s"},
		checkerdef.CheckTypeMongoDB:      {"host": host, "port": port, "timeout": "1s"},
		checkerdef.CheckTypeClickHouse:   {"host": host, "port": port},
		checkerdef.CheckTypePOP3:         {"host": host, "port": port},
		checkerdef.CheckTypeIMAP:         {"host": host, "port": port},
		checkerdef.CheckTypeFTP:          {"host": host, "port": port},
		checkerdef.CheckTypeSFTP:         {"host": host, "port": port, "username": "u", "password": "p"},
		checkerdef.CheckTypeGRPC:         {"host": host, "port": port},
		checkerdef.CheckTypeKafka:        {"brokers": []any{ln.Addr().String()}},
		checkerdef.CheckTypeMQTT:         {"host": host, "port": port},
		checkerdef.CheckTypeRabbitMQ:     {"host": host, "port": port},
		checkerdef.CheckTypeRDP:          {"host": host, "port": port},
		checkerdef.CheckTypeVNC:          {"host": host, "port": port},
		checkerdef.CheckTypeSIP:          {"host": host, "port": port, "transport": "tcp"},
		checkerdef.CheckTypeUDP:          {"host": host, "port": port},
		checkerdef.CheckTypeNTP:          {"host": host, "port": port},
		checkerdef.CheckTypeSNMP:         {"host": host, "port": port},
		checkerdef.CheckTypeA2S:          {"host": host, "port": port},
		checkerdef.CheckTypeMinecraft:    {"host": host, "port": port},
		checkerdef.CheckTypeICMP:         {"host": host},
		checkerdef.CheckTypeDNS:          {"host": "acme.com", "nameserver": host + ":53"},
		checkerdef.CheckTypeDNSBL:        {"target": "8.8.8.8", "nameserver": host + ":53"},
		checkerdef.CheckTypeWebSocket:    {"url": "ws://" + ln.Addr().String() + "/"},
		checkerdef.CheckTypeDocker:       {"host": "tcp://" + ln.Addr().String(), "containerName": "web"},
		checkerdef.CheckTypeJS: {"script": fmt.Sprintf(
			`var r = solidping.check("http", { url: %q }); return { status: r.status, output: r.output };`,
			httpServer.URL,
		)},
	}

	for checkType, cfgMap := range cases {
		t.Run(string(checkType), func(t *testing.T) {
			t.Parallel()

			denied, text := runRegistered(t, egress.New(false), checkType, cfgMap)
			require.NotNil(t, denied, "%s must be refused by the guard, got %s", checkType, text)

			allowed, text := runRegistered(t, egress.New(true), checkType, cfgMap)
			require.Nil(t, allowed, "%s must not be refused by a permissive guard, got %s", checkType, text)
			require.NotContains(t, text, "denied by egress policy")
		})
	}
}

// A docker sub-check nested in a js script used to dial its host on the
// docker client's own transport, ignoring the guard on the execution context
// (spec 2026-10-16-09): a script could probe the worker's local socket or any
// internal tcp:// endpoint. Both must now be refused with an egress denial;
// the permissive run of the same script is the positive control.
func TestJSDockerSubCheckIsRefusedByAnEnforcingGuard(t *testing.T) {
	t.Parallel()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}

			_ = conn.Close()
		}
	}()

	hosts := map[string]string{
		"tcp":  "tcp://" + ln.Addr().String(),
		"unix": "unix:///should-never-be-dialed-2026-10-16-09.sock",
	}

	for name, host := range hosts {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfgMap := map[string]any{"script": fmt.Sprintf(
				`var r = solidping.check("docker", { host: %q, containerName: "web" }); `+
					`return { status: r.status, output: r.output };`,
				host,
			)}

			denied, text := runRegistered(t, egress.New(false), checkerdef.CheckTypeJS, cfgMap)
			require.NotNil(t, denied, "a nested docker sub-check must be refused by the guard, got %s", text)

			allowed, text := runRegistered(t, egress.New(true), checkerdef.CheckTypeJS, cfgMap)
			require.Nil(t, allowed, "a permissive guard must not refuse it, got %s", text)
			require.NotContains(t, text, "denied by egress policy")
		})
	}
}

// A MySQL password injecting its own `@tcp(<loopback>)/` used to consume the
// one-shot DSN rewrite to the guarded network, so the real dial went out on a
// plain net.Dialer with no refusal recorded (spec 2026-10-16-04). The guard
// must still refuse the loopback target; the permissive run is the control.
func TestMySQLCraftedPasswordCannotShedTheEgressGuard(t *testing.T) {
	t.Parallel()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}

			_ = conn.Close()
		}
	}()

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)

	cfgMap := map[string]any{
		"host":     host,
		"port":     port,
		"username": "u",
		"password": "X@tcp(" + ln.Addr().String() + ")/e",
		"timeout":  "2s",
	}

	denied, text := runRegistered(t, egress.New(false), checkerdef.CheckTypeMySQL, cfgMap)
	require.NotNil(t, denied, "a crafted password must not shed the guard, got %s", text)

	allowed, text := runRegistered(t, egress.New(true), checkerdef.CheckTypeMySQL, cfgMap)
	require.Nil(t, allowed, "a permissive guard must not refuse, got %s", text)
	require.NotContains(t, text, "denied by egress policy")
}
