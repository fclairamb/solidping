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
		checkerdef.CheckTypeHTTP:       {"url": httpServer.URL},
		checkerdef.CheckTypePrometheus: {"url": httpServer.URL + "/metrics"},
		checkerdef.CheckTypeTCP:        {"host": host, "port": port},
		checkerdef.CheckTypeSSL:        {"host": host, "port": port},
		checkerdef.CheckTypeSSH:        {"host": host, "port": port},
		checkerdef.CheckTypeSMTP:       {"host": host, "port": port},
		checkerdef.CheckTypeRedis:      {"host": host, "port": port},
		checkerdef.CheckTypePostgreSQL: {"host": host, "port": port, "username": "u", "database": "d"},
		checkerdef.CheckTypeMySQL:      {"host": host, "port": port, "username": "u"},
		checkerdef.CheckTypeMSSQL:      {"host": host, "port": port, "username": "u", "password": "p"},
		checkerdef.CheckTypeOracle:     {"host": host, "port": port, "username": "u", "password": "p", "serviceName": "s"},
		checkerdef.CheckTypeMongoDB:    {"host": host, "port": port, "timeout": "1s"},
		checkerdef.CheckTypeClickHouse: {"host": host, "port": port},
		checkerdef.CheckTypePOP3:       {"host": host, "port": port},
		checkerdef.CheckTypeIMAP:       {"host": host, "port": port},
		checkerdef.CheckTypeFTP:        {"host": host, "port": port},
		checkerdef.CheckTypeSFTP:       {"host": host, "port": port, "username": "u", "password": "p"},
		checkerdef.CheckTypeGRPC:       {"host": host, "port": port},
		checkerdef.CheckTypeKafka:      {"brokers": []any{ln.Addr().String()}},
		checkerdef.CheckTypeMQTT:       {"host": host, "port": port},
		checkerdef.CheckTypeRabbitMQ:   {"host": host, "port": port},
		checkerdef.CheckTypeRDP:        {"host": host, "port": port},
		checkerdef.CheckTypeSIP:        {"host": host, "port": port, "transport": "tcp"},
		checkerdef.CheckTypeUDP:        {"host": host, "port": port},
		checkerdef.CheckTypeNTP:        {"host": host, "port": port},
		checkerdef.CheckTypeSNMP:       {"host": host, "port": port},
		checkerdef.CheckTypeA2S:        {"host": host, "port": port},
		checkerdef.CheckTypeMinecraft:  {"host": host, "port": port},
		checkerdef.CheckTypeICMP:       {"host": host},
		checkerdef.CheckTypeDNS:        {"host": "acme.com", "nameserver": host + ":53"},
		checkerdef.CheckTypeDNSBL:      {"target": "8.8.8.8", "nameserver": host + ":53"},
		checkerdef.CheckTypeWebSocket:  {"url": "ws://" + ln.Addr().String() + "/"},
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
