package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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