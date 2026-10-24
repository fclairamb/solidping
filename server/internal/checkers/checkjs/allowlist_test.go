package checkjs

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/egress"
)

// runAllowlisted runs a script held to hosts, and returns its result and the
// allowlist's first refusal.
func runAllowlisted(t *testing.T, hosts []string, script string) (*checkerdef.Result, *egress.HostNotAllowedError) {
	t.Helper()

	list := egress.NewHostAllowlist(hosts)
	ctx := egress.WithHostAllowlist(context.Background(), list)

	result, err := (&JSChecker{}).Execute(ctx, &JSConfig{Script: script})
	require.NoError(t, err)

	return result, list.Refused()
}

func portOf(t *testing.T, addr string) string {
	t.Helper()

	_, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)

	return port
}

func TestAllowlistHoldsEveryNetworkAPI(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32

	var port string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/redirect" {
			http.Redirect(w, req, "http://localhost:"+port+"/", http.StatusFound)

			return
		}

		hits.Add(1)
	}))
	t.Cleanup(server.Close)

	port = portOf(t, server.Listener.Addr().String())

	cases := []struct {
		name, script, refused string
	}{
		{
			name: "http built at runtime",
			script: `var r = http.get("http:" + "//local" + "host:` + port + `/"); ` +
				`return { status: "up", output: { e: r.error } };`,
			refused: "localhost",
		},
		{
			name: "redirect to another host",
			script: `var r = http.get("` + server.URL + `/redirect"); ` +
				`return { status: "up", output: { e: r.error } };`,
			refused: "localhost",
		},
		{
			name:    "tcp",
			script:  `var c = tcp.connect("localhost:` + port + `"); return { status: "up", output: { e: c.error } };`,
			refused: "localhost",
		},
		{
			name: "websocket",
			script: `var c = websocket.connect("ws:" + "//localhost:` + port + `/"); ` +
				`return { status: "up", output: { e: c.error } };`,
			refused: "localhost",
		},
		{
			name: "sub-check",
			script: `var c = solidping.http({ url: "` + server.URL + `" }); ` +
				`return { status: "up", output: { e: c.output.error } };`,
			refused: "solidping.http",
		},
		{
			name: "browser",
			script: `try { browser.open(); } catch (e) { return { status: "up", output: { e: String(e) } }; } ` +
				`return { status: "up" };`,
			refused: "browser",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			result, refused := runAllowlisted(t, []string{"127.0.0.1"}, tc.script)
			r.NotNil(refused, result.Output)
			r.Equal(tc.refused, refused.Target)
			r.Contains(fmt.Sprint(result.Output["e"]), "host not allowed while secrets are loaded")
		})
	}

	t.Cleanup(func() {
		require.Zero(t, hits.Load(), "no refused connection reached the server")
	})
}

// Positive control: the allowed host is reached, through a URL built at
// runtime too, and no allowlist at all leaves every host reachable.
func TestAllowlistLetsTheAllowedHostThrough(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var hits atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(server.Close)

	port := portOf(t, server.Listener.Addr().String())
	script := `var r = http.get("http:" + "//127.0.0." + "1:` + port + `/"); ` +
		`var c = tcp.connect("127.0.0.1:` + port + `"); if (c.ok) { c.close(); } ` +
		`return { status: r.error || !c.ok ? "down" : "up", output: { e: r.error || c.error } };`

	result, refused := runAllowlisted(t, []string{"127.0.0.1"}, script)
	r.Nil(refused)
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	r.Equal(int32(1), hits.Load())

	unrestricted, err := (&JSChecker{}).Execute(context.Background(), &JSConfig{
		Script: `var r = http.get("http:" + "//local" + "host:` + port + `/"); return { status: r.error ? "down" : "up" };`,
	})
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, unrestricted.Status, unrestricted.Output)
	r.Equal(int32(2), hits.Load())
}
