package checkjs

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/egress"
)

const jsInternalSecret = "internal-metadata-secret"

// runEgressScript runs a script with the given egress guard on its context.
func runEgressScript(t *testing.T, guard *egress.Guard, script string) *checkerdef.Result {
	t.Helper()

	ctx := egress.WithGuard(context.Background(), guard)

	result, err := (&JSChecker{}).Execute(ctx, &JSConfig{Script: script})
	require.NoError(t, err)
	require.NotNil(t, result)

	return result
}

// A script's http.get towards a loopback service is refused under an
// enforcing policy: the request's error carries the policy message and no
// byte of the internal response ever reaches the script (and so the result).
func TestScriptHTTPGetIsRefusedUnderAnEnforcingPolicy(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(jsInternalSecret))
	}))
	t.Cleanup(server.Close)

	script := fmt.Sprintf(`
var res = http.get(%q);
return { status: res.error ? "down" : "up", output: { error: res.error, body: res.body } };
`, server.URL+"/latest/meta-data/iam/")

	denied := runEgressScript(t, egress.New(false), script)
	r.Contains(fmt.Sprint(denied.Output["error"]), "denied by egress policy", denied.Output)
	r.Nil(denied.Output["body"], "no body in output")
	r.NotContains(fmt.Sprint(denied.Output), jsInternalSecret)

	// Positive control: same script, permissive policy.
	allowed := runEgressScript(t, egress.New(true), script)
	r.Equal(checkerdef.StatusUp, allowed.Status, allowed.Output)
	r.Equal(jsInternalSecret, allowed.Output["body"])
}

// Raw sockets go through the guard too (the pinned, resolved address is
// judged before connect).
func TestScriptSocketIsRefusedUnderAnEnforcingPolicy(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	r.NoError(err)
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

	script := fmt.Sprintf(`
var c = tcp.connect(%q);
if (!c.ok) return { status: "down", output: { error: c.error } };
c.close();
return { status: "up" };
`, ln.Addr().String())

	denied := runEgressScript(t, egress.New(false), script)
	r.Equal(checkerdef.StatusDown, denied.Status)
	r.Contains(fmt.Sprint(denied.Output["error"]), "denied by egress policy")

	allowed := runEgressScript(t, egress.New(true), script)
	r.Equal(checkerdef.StatusUp, allowed.Status, allowed.Output)
}
