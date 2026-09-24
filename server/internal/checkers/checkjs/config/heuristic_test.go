package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// TestJSMinPeriodHintRDP pins the rdp.connect heuristic on JSConfig: the
// call raises the floor to the 15-minute authenticated floor, a script
// without one keeps the global floor.
func TestJSMinPeriodHintRDP(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	withCall := &JSConfig{Script: "const s = rdp.connect({host:'rdp.acme.com', username:'u', password:'p'});"}
	r.Equal(15*time.Minute, withCall.MinPeriodHint())

	without := &JSConfig{Script: "return { status: 'up' };"}
	r.Equal(time.Duration(0), without.MinPeriodHint())
}

// TestJSMinPeriodHintSource pins which floor the refusal message names: the
// RDP one whenever rdp.connect is called (it is the higher floor), the
// browser one for a browser-only script, nothing otherwise.
func TestJSMinPeriodHintSource(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	rdpOnly := &JSConfig{Script: "const s = rdp.connect({host:'h'});"}
	r.Equal(checkerdef.MinPeriodSourceRDP, rdpOnly.MinPeriodHintSource())

	both := &JSConfig{Script: "const p = browser.open(); const s = rdp.connect({host:'h'});"}
	r.Equal(checkerdef.MinPeriodSourceRDP, both.MinPeriodHintSource())
	r.Equal(15*time.Minute, both.MinPeriodHint())

	browserOnly := &JSConfig{Script: "const p = browser.open();"}
	r.Equal(checkerdef.MinPeriodSourceBrowser, browserOnly.MinPeriodHintSource())

	plain := &JSConfig{Script: "return { status: 'up' };"}
	r.Empty(plain.MinPeriodHintSource())
}
