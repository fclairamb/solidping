// patch_solidping.go carries the SolidPing patches to this vendored grdp
// fork, kept in ONE file deliberately: every SolidPing need that upstream
// grdp does not already expose lives here, and nothing else in the fork is
// modified beyond the import-path rewrite and the deletion of the cgo-only
// codec plugins. See README-solidping.md.
package grdp

import (
	"io"

	"github.com/fclairamb/solidping/server/third_party/grdp/protocol/pdu"
)

// SendUnicodeKey sends one Unicode character as a TS_UNICODE_KEYBOARD_EVENT
// press followed by its release (MS-RDPBCGR §2.2.8.1.2.2.5 fast-path, with a
// slow-path fallback through SendInputEvents).
//
// Unicode input is what `type()` is built on: it carries the character
// itself, so the target's keyboard layout is irrelevant — "hello" arrives as
// h-e-l-l-o on a French AZERTY exactly as on a US QWERTY. `key()` uses
// scancodes instead, because named keys and combos (Enter, ctrl+alt+end) have
// no Unicode position.
//
// No-op when the session is not ready: an event sent before the connection
// sequence finishes is dropped by the server, and silently dropping it here
// keeps the check's script from having to poll readiness between actions.
func (g *RdpClient) SendUnicodeKey(r rune) {
	if !g.eventReady.Load() || g.pdu == nil {
		return
	}

	g.flushMouseMove()
	g.flushWheel()

	press := &pdu.UnicodeKeyEvent{Unicode: uint16(r)}
	release := &pdu.UnicodeKeyEvent{
		Unicode: uint16(r),
		// KBDFLAGS_RELEASE marks the key-up half of the pair.
		KeyboardFlags: pdu.KBDFLAGS_RELEASE,
	}

	g.pdu.SendInputEvents(pdu.INPUT_EVENT_UNICODE, []pdu.InputEventsInterface{press, release})
}

// SendLogoff asks the SERVER to end the session by sending the
// TS_SHUTDOWN_REQUEST data PDU (MS-RDPBCGR 2.2.9.3 — the protocol's "log me
// off" verb; the server acknowledges with TS_SHUTDOWN_DENIED-prefixed
// teardown and removes the session). This is what `end_session: logoff`
// sends, and what a script's logoff() triggers.
//
// Compare Close(), which only drops the transport: that is a DISCONNECT —
// the session survives on the server until its idle policy ends it, and the
// next connect reattaches to it.
//
// No-op when the session is not ready: a log-off request before the session
// exists is meaningless, and the caller's logoff error reporting treats a
// closed session the same way.
func (g *RdpClient) SendLogoff() {
	if !g.eventReady.Load() || g.pdu == nil {
		return
	}

	// ShutdownRequestPDU is empty (§2.2.9.3): the data PDU's type alone
	// carries the request. Declared here rather than in upstream's data.go so
	// the patch stays in one file.
	g.pdu.SendDataPDU(&shutdownRequestPDU{})
}

// shutdownRequestPDU is the empty TS_SHUTDOWN_REQUEST data PDU.
type shutdownRequestPDU struct{}

func (*shutdownRequestPDU) Type2() uint8 { return pdu.PDUTYPE2_SHUTDOWN_REQUEST }

func (*shutdownRequestPDU) Unpack(_ io.Reader) error { return nil }

// SendScancode sends one scancode key event: press when release is false,
// release when true. sc may carry the 0xE0 extended prefix (0xE04B for Left
// Arrow); it is stripped and translated into KBDFLAGS_EXTENDED, which is the
// form both the slow-path serializer and the fast-path encoder expect.
//
// `key()` composes named keys and combos out of these — scancodes, not
// Unicode, because named keys and combos (Enter, ctrl+alt+end, win+r) have no
// Unicode position.
func (g *RdpClient) SendScancode(sc uint16, release bool) {
	if !g.eventReady.Load() || g.pdu == nil {
		return
	}

	g.flushMouseMove()
	g.flushWheel()

	p := &pdu.ScancodeKeyEvent{}
	if sc&0xFF00 == 0xE000 {
		p.KeyCode = sc & 0x00FF
		p.KeyboardFlags |= pdu.KBDFLAGS_EXTENDED
	} else {
		p.KeyCode = sc
	}

	if release {
		p.KeyboardFlags |= pdu.KBDFLAGS_RELEASE
	}

	g.pdu.SendInputEvents(pdu.INPUT_EVENT_SCANCODE, []pdu.InputEventsInterface{p})
	g.notifyGfxLocalInput()
}

// EventReady reports whether the session accepts input events. A script can
// poll it instead of acting blind after a session drop.
func (g *RdpClient) EventReady() bool {
	return g.eventReady.Load()
}
