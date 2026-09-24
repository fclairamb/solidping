package checkjs

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checkrdpconfig "github.com/fclairamb/solidping/server/internal/checkers/checkrdp/config"
)

// TestAuthenticatedRDPSubCheckIsRefused pins that the 15-minute floor holds
// however a script reaches an authenticated logon. The validation-time
// heuristic only sees `rdp.connect(` in the script text; a sub-check's config
// is built at run time, so solidping.rdp({username, password}) and
// solidping.check("rdp", …) must be refused at run time — and the checker
// must never be entered.
//
//nolint:paralleltest // mutates the package-level ResolveChecker global
func TestAuthenticatedRDPSubCheckIsRefused(t *testing.T) {
	r := require.New(t)

	stub := &stubChecker{checkType: checkerdef.CheckTypeRDP}

	previous := ResolveChecker
	t.Cleanup(func() { ResolveChecker = previous })

	ResolveChecker = func(checkType checkerdef.CheckType) (checkerdef.Checker, checkerdef.Config, bool) {
		if checkType != checkerdef.CheckTypeRDP {
			return nil, nil, false
		}

		// The REAL rdp config, so the refusal is driven by its actual
		// MinPeriodHint and not by a test double's say-so.
		return stub, &checkrdpconfig.RDPConfig{}, true
	}

	for name, call := range map[string]string{
		"typed wrapper": `solidping.rdp({host: "rdp.acme.com", username: "u", password: "p"})`,
		"generic check": `solidping.check("rdp", {host: "rdp.acme.com", username: "u", password: "p"})`,
	} {
		result, err := (&JSChecker{}).Execute(t.Context(), &JSConfig{
			Script:  `var res = ` + call + `; return { status: "up", output: { sub: res.status, err: res.output.error } };`,
			Timeout: 5 * time.Second,
		})
		r.NoError(err, name)
		r.Equal("error", result.Output["sub"], name)
		r.Contains(result.Output["err"], "Use rdp.connect()", name)
	}

	r.Zero(stub.calls.Load(), "an authenticated logon must never have been attempted")

	// Positive control: the pre-auth handshake (no credentials) has no floor
	// of its own and still runs as a sub-check.
	result, err := (&JSChecker{}).Execute(t.Context(), &JSConfig{
		Script:  `var res = solidping.rdp({host: "rdp.acme.com"}); return { status: res.status };`,
		Timeout: 5 * time.Second,
	})
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status)
	r.Equal(int32(1), stub.calls.Load())
}
