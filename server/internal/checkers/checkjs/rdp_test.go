package checkjs

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkrdp"
	checkrdpconfig "github.com/fclairamb/solidping/server/internal/checkers/checkrdp/config"
)

// runRDPScript executes a script against a fake RDP session and returns the
// result. Same style as runBrowserScript: the seams are package-level and the
// tests that mutate them are not parallel.
func runRDPScript(t *testing.T, script string, timeout time.Duration) *checkerdef.Result {
	t.Helper()

	checker := &JSChecker{}

	result, err := checker.Execute(t.Context(), &JSConfig{Script: script, Timeout: timeout})
	require.NoError(t, err)
	require.NotNil(t, result)

	return result
}

// connect is the prefix every script starts with.
const connect = `var s = rdp.connect({host:"rdp.acme.com", username:"u", password:"p"});`

// TestRDPBindingsReturnShapes pins every method's return shape against a fake
// session — the contract a script reads, method by method.
//
//nolint:paralleltest // mutates the package-level OpenRDPSession seam
func TestRDPBindingsReturnShapes(t *testing.T) {
	r := require.New(t)

	session := &fakeRDPSession{pixel: checkrdp.PixelColor{R: 200, G: 100, B: 50}}
	openRDPTestSession(t, session)

	result := runRDPScript(t, connect+`
var stable = s.waitForStable({quietMs: 1});
var change = s.waitForChange(50);
var typed = s.type("hello");
var key = s.key("enter");
var click = s.click(640, 400);
var right = s.rightClick(640, 400);
var dbl = s.doubleClick(640, 400);
var moved = s.move(10, 20);
var px = s.pixel(10, 10);
var hash = s.regionHash(0, 0, 200, 50);
var shot = s.screenshot();
var end = s.logoff();
return { status: "up", output: {
	stableOk: stable.ok, typeOk: typed.ok, keyOk: key.ok, clickOk: click.ok,
	moveOk: moved.ok, pixelOk: px.ok, hashOk: hash.ok, shotOk: shot.ok,
	endOk: end.ok, r: px.r, g: px.g, b: px.b, hash: hash.hash,
	changeErr: change.error,
} };`, 5*time.Second)

	out := result.Output
	r.Equal("up", result.Status.String())
	r.Equal(true, out["stableOk"])
	r.Equal(true, out["typeOk"])
	r.Equal(true, out["keyOk"])
	r.Equal(true, out["clickOk"])
	r.Equal(true, out["moveOk"])
	r.Equal(true, out["pixelOk"])
	r.Equal(true, out["hashOk"])
	r.Equal(true, out["shotOk"])
	r.Equal(true, out["endOk"])
	r.Equal(int64(200), out["r"], "goja widens uint8 to int64 across the boundary")
	r.Equal(int64(100), out["g"])
	r.Equal(int64(50), out["b"])
	r.Equal(int64(0x1234), out["hash"], "goja widens uint64 to int64 across the boundary")
}

// TestRDPDriverSequencePinned pins the call ORDER of a full flow.
//
//nolint:paralleltest // mutates the package-level OpenRDPSession seam
func TestRDPDriverSequence(t *testing.T) {
	r := require.New(t)

	session := &fakeRDPSession{}
	openRDPTestSession(t, session)

	result := runRDPScript(t, connect+`
s.waitForStable();
s.type("hello");
s.key("ctrl+alt+end");
s.click(640, 400);
s.screenshot();
s.logoff();
return { status: "up" };`, 5*time.Second)

	r.Equal("up", result.Status.String())

	joined := strings.Join(session.calls, ";")
	for _, want := range []string{
		"waitForStable", "type(hello)", "key(ctrl+alt+end)", "click(640,400)", "screenshot", "logoff",
	} {
		r.Contains(joined, want)
	}
}

// TestRDPConnectAuthFailureIsAValue pins the open contract: a rejected
// password comes back as `{ ok: false, failureCode }`, not a throw — the
// script decides the target is down.
//
//nolint:paralleltest // mutates the package-level OpenRDPSession seam
func TestRDPConnectAuthFailureIsAValue(t *testing.T) {
	r := require.New(t)

	previous := checkrdp.OpenRDPSession
	t.Cleanup(func() { checkrdp.OpenRDPSession = previous })

	checkrdp.OpenRDPSession = func(
		_ context.Context, _ *checkrdpconfig.RDPConfig, _, _ int, _ net.Conn,
	) (checkrdp.RDPSession, error) {
		return nil, &checkrdp.ErrAuthFailure{
			Reason: checkrdp.AuthRejected,
			Msg:    "logon failed: the server rejected the credentials (CredSSP/NLA)",
		}
	}

	result := runRDPScript(t, `
const s = rdp.connect({host:"rdp.acme.com", username:"u", password:"bad"});
return { status: s.ok ? "up" : "down", output: { code: s.failureCode } };`, 5*time.Second)

	r.Equal("down", result.Status.String())
	r.Equal("auth_rejected", result.Output["code"])
}

// TestRDPConnectSlotTimeoutIsAValue pins the slot contract: a full worker is
// a `{ timedOut: true }` value, not a throw — the script reports the timeout.
//
//nolint:paralleltest // mutates the package-level OpenRDPSession seam
func TestRDPConnectSlotTimeoutIsAValue(t *testing.T) {
	r := require.New(t)

	previous := checkrdp.OpenRDPSession
	t.Cleanup(func() { checkrdp.OpenRDPSession = previous })

	checkrdp.OpenRDPSession = func(
		_ context.Context, _ *checkrdpconfig.RDPConfig, _, _ int, _ net.Conn,
	) (checkrdp.RDPSession, error) {
		return nil, checkrdp.ErrSlotTimeout
	}

	result := runRDPScript(t, `
const s = rdp.connect({host:"rdp.acme.com", username:"u", password:"p"});
return { status: s.timedOut ? "timeout" : "up", output: { timedOut: s.timedOut === true } };`, 5*time.Second)

	r.Equal("timeout", result.Status.String())
	r.Equal(true, result.Output["timedOut"])
}

// TestRDPConnectScriptBugThrows pins the throw paths: a second session, a
// missing credential pair.
//
//nolint:paralleltest // mutates the package-level OpenRDPSession seam
func TestRDPConnectScriptBugThrows(t *testing.T) {
	r := require.New(t)

	session := &fakeRDPSession{}
	openRDPTestSession(t, session)

	result := runRDPScript(t, connect+`
try { rdp.connect({host:"rdp.acme.com", username:"u", password:"p"}); return {status:"up", output:{second:false}}; }
catch (e) { return {status:"down", output:{second:true}}; }`, 5*time.Second)

	r.Equal("down", result.Status.String())
	r.Equal(true, result.Output["second"])

	result2 := runRDPScript(t, `
try { rdp.connect({host:"rdp.acme.com"}); return {status:"up"}; }
catch (e) { return {status:"down", output:{needsCreds: String(e).includes("username and password")}}; }`, 5*time.Second)

	r.Equal("down", result2.Status.String())
	r.Equal(true, result2.Output["needsCreds"])
}

// TestRDPConnectRefusedWhenTypeDisabled mirrors the browser gate test: the
// scripted path answers with the EXACT string sub-checks use.
//
//nolint:paralleltest // mutates the package-level TypeEnabled and OpenRDPSession seams
func TestRDPConnectRefusedWhenTypeDisabled(t *testing.T) {
	r := require.New(t)

	session := &fakeRDPSession{}
	openRDPTestSession(t, session)
	installGate(t, checkerdef.CheckTypeRDP)

	result := runRDPScript(t, connect+`return { status: "up" };`, 5*time.Second)

	r.Equal("error", result.Status.String())
	r.Contains(result.Output["error"], `check type "rdp" is disabled on this server`)
	r.Empty(session.calls, "the session must never have been opened")
}

// TestRDPUncountedEndMethodsAndImplicitLogoff pins that logoff/disconnect/
// close never spend an action, close() is logoff()'s alias, and a script that
// ends without calling either gets a log off from the runtime's defer.
//
//nolint:paralleltest // mutates the package-level OpenRDPSession seam
func TestRDPUncountedEndMethodsAndImplicitLogoff(t *testing.T) {
	r := require.New(t)

	session := &fakeRDPSession{}
	openRDPTestSession(t, session)

	result := runRDPScript(t, connect+`
return { status: "up" };`, 5*time.Second)

	r.Equal("up", result.Status.String())
	r.Contains(strings.Join(session.calls, ";"), "logoff", "the defer ended the session")
}

// TestRDPInfraThrowsChangeTimeoutReturns pins the throw-vs-return split: a
// dead session throws (error), a change timeout is a returned value.
//
//nolint:paralleltest // mutates the package-level OpenRDPSession seam
func TestRDPInfraThrowsChangeTimeoutReturns(t *testing.T) {
	r := require.New(t)

	// A session whose driver reports ErrNotReady — infrastructure → throw.
	deadSession := &fakeRDPSession{inputErr: checkrdp.ErrNotReady}
	openRDPTestSession(t, deadSession)

	result := runRDPScript(t, connect+`
try { s.type("x"); return { status: "up" }; }
catch (e) { return { status: "error", output: { caught: String(e).includes("not ready") } }; }`, 5*time.Second)

	r.Equal("error", result.Status.String())
	r.Equal(true, result.Output["caught"])

	// A stuck screen: waitForChange times out as a VALUE.
	stuck := &fakeRDPSession{changeErr: checkrdp.ErrChangeTimedOut}
	openRDPTestSession(t, stuck)

	result2 := runRDPScript(t, connect+`
const w = s.waitForChange(50);
return { status: w.ok ? "up" : "down", output: { err: w.error } };`, 5*time.Second)

	r.Equal("down", result2.Status.String())
	r.Contains(result2.Output["err"], "no screen change")
}

// TestRDPScreenshotFeedsBrowserCapture pins that rdp.screenshot() feeds the
// browser capture path (last wins), so a shot on a failing run is kept.
//
//nolint:paralleltest // mutates the package-level OpenRDPSession seam
func TestRDPScreenshotFeedsBrowserCapture(t *testing.T) {
	r := require.New(t)

	session := &fakeRDPSession{}
	openRDPTestSession(t, session)

	result := runRDPScript(t, connect+`s.screenshot(); return { status: "down" };`, 5*time.Second)

	r.Equal("down", result.Status.String())
	r.Equal(1, session.screenshotN)
	r.NotNil(result.Diagnostics, "the capture rides the browser Diagnostics path")
	r.NotNil(result.Diagnostics.Screenshot)
	r.Equal([]byte("fake-png"), result.Diagnostics.Screenshot.Image)
}
