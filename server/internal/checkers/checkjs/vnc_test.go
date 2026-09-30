package checkjs

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkvnc"
)

const vncConnectJS = `var s = vnc.connect({host:"vnc.acme.com", password:"pw"});`

func runVNCScript(t *testing.T, script string) *checkerdef.Result {
	t.Helper()

	result, err := (&JSChecker{}).Execute(t.Context(), &JSConfig{Script: script, Timeout: 5 * time.Second})
	require.NoError(t, err)
	require.NotNil(t, result)

	return result
}

//nolint:paralleltest // mutates the package-level OpenVNCSession seam
func TestVNCConnectClickTypeScreenshot(t *testing.T) {
	r := require.New(t)

	session := &fakeVNCSession{pixel: checkvnc.PixelColor{R: 200, G: 100, B: 50}}
	openVNCTestSession(t, session)

	result := runVNCScript(t, vncConnectJS+`
s.waitForStable({quietMs: 1});
s.click(10, 20);
s.type("hello");
s.key("ctrl+alt+delete");
var px = s.pixel(1, 2);
var h = s.regionHash(0, 0, 5, 5);
var shot = s.screenshot();
return { status: "up", output: { r: px.r, hash: h.hash, shot: shot.ok } };`)

	r.Equal("up", result.Status.String(), result.Output)
	r.Equal(int64(200), result.Output["r"])
	r.Equal(int64(0x1234), result.Output["hash"])
	r.Equal(true, result.Output["shot"])
	r.Equal([]string{
		"waitForStable", "click(10,20)", "type(hello)", "key(ctrl+alt+delete)",
		"pixel(1,2)", "regionHash(0,0,5,5)", "screenshot", "end",
	}, session.snapshot())
}

//nolint:paralleltest // mutates the package-level OpenVNCSession seam
func TestVNCSecondConnectIsRefused(t *testing.T) {
	r := require.New(t)

	session := &fakeVNCSession{}
	opened := openVNCTestSession(t, session)

	result := runVNCScript(t, vncConnectJS+`
try { vnc.connect({host:"vnc.acme.com"}); return {status:"up", output:{second:false}}; }
catch (e) { return {status:"down", output:{second: String(e).includes("already open")}}; }`)

	r.Equal("down", result.Status.String())
	r.Equal(true, result.Output["second"])
	r.Equal(1, *opened)
}

//nolint:paralleltest // mutates the package-level OpenVNCSession seam
func TestVNCConnectWithoutHostIsRefused(t *testing.T) {
	r := require.New(t)

	opened := openVNCTestSession(t, &fakeVNCSession{})

	result := runVNCScript(t, `
try { vnc.connect({password:"pw"}); return {status:"up"}; }
catch (e) { return {status:"down", output:{needsHost: String(e).includes("host is required")}}; }`)

	r.Equal("down", result.Status.String())
	r.Equal(true, result.Output["needsHost"])
	r.Zero(*opened)
}

//nolint:paralleltest // mutates the package-level OpenVNCSession seam
func TestVNCActionBudgetExhausted(t *testing.T) {
	r := require.New(t)

	session := &fakeVNCSession{}
	openVNCTestSession(t, session)

	result := runVNCScript(t, vncConnectJS+`
var last;
for (var i = 0; i < 101; i++) { last = s.move(1, 1); }
return { status: last.ok ? "up" : "down", output: { err: last.error } };`)

	r.Equal("down", result.Status.String())
	r.Contains(result.Output["err"], "vnc action limit of 100 exceeded")
	// connect spent one unit, so 99 moves ran before the budget ran out.
	moves := 0

	for _, call := range session.snapshot() {
		if strings.HasPrefix(call, "move") {
			moves++
		}
	}

	r.Equal(99, moves)
}

//nolint:paralleltest // mutates the package-level OpenVNCSession seam
func TestVNCThrowingScriptStillClosesSession(t *testing.T) {
	r := require.New(t)

	session := &fakeVNCSession{}
	openVNCTestSession(t, session)

	result := runVNCScript(t, vncConnectJS+`throw new Error("boom");`)

	r.Equal("error", result.Status.String())
	r.True(session.Closed(), "the deferred close must end the session")
	r.Equal("end", session.snapshot()[len(session.snapshot())-1])
}

//nolint:paralleltest // mutates the package-level OpenVNCSession seam
func TestVNCConnectFailureIsAValue(t *testing.T) {
	r := require.New(t)

	previous := checkvnc.OpenVNCSession
	t.Cleanup(func() { checkvnc.OpenVNCSession = previous })

	checkvnc.OpenVNCSession = func(_ context.Context, _ *checkvnc.VNCConfig, _ net.Conn) (checkvnc.VNCSession, error) {
		return nil, &checkvnc.FailureError{Code: checkvnc.FailureAuthFailed, Msg: "authentication failed"}
	}

	result := runVNCScript(t, `
var s = vnc.connect({host:"vnc.acme.com", password:"bad"});
return { status: s.ok ? "up" : "down", output: { code: s.failureCode } };`)

	r.Equal("down", result.Status.String())
	r.Equal("AUTH_FAILED", result.Output["code"])
}

//nolint:paralleltest // mutates the package-level TypeEnabled and OpenVNCSession seams
func TestVNCConnectRefusedWhenTypeDisabled(t *testing.T) {
	r := require.New(t)

	session := &fakeVNCSession{}
	openVNCTestSession(t, session)
	installGate(t, checkerdef.CheckTypeVNC)

	result := runVNCScript(t, vncConnectJS+`return { status: "up" };`)

	r.Equal("error", result.Status.String())
	r.Contains(result.Output["error"], `check type "vnc" is disabled on this server`)
	r.Empty(session.snapshot())
}

//nolint:paralleltest // mutates the package-level OpenVNCSession seam
func TestVNCInfraThrowsChangeTimeoutReturns(t *testing.T) {
	r := require.New(t)

	openVNCTestSession(t, &fakeVNCSession{inputErr: checkvnc.ErrSessionDead})

	result := runVNCScript(t, vncConnectJS+`
try { s.type("x"); return { status: "up" }; }
catch (e) { return { status: "error", output: { caught: String(e).includes("gone") } }; }`)

	r.Equal("error", result.Status.String())
	r.Equal(true, result.Output["caught"])

	openVNCTestSession(t, &fakeVNCSession{changeErr: checkvnc.ErrChangeTimedOut})

	result = runVNCScript(t, vncConnectJS+`
var w = s.waitForChange(50);
return { status: w.ok ? "up" : "down", output: { err: w.error } };`)

	r.Equal("down", result.Status.String())
	r.Contains(result.Output["err"], "no screen change")
}
