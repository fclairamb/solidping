package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// rdpFloorReason is the sentence the refusal must carry for an authenticated
// RDP run — a `js` script calling rdp.connect or an rdp check with credentials.
const rdpFloorReasonText = "authenticated RDP runs are real interactive logons"

func rdpCheckRequest(slug, period string, withCredentials bool) checks.CreateCheckRequest {
	config := map[string]any{"host": "rdp.acme.com"}
	if withCredentials {
		config["username"] = "svc-monitor"
		config["password"] = "hunter2"
	}

	req := checks.CreateCheckRequest{Name: slug, Slug: slug, Type: "rdp", Config: config}
	if period != "" {
		req.Period = &period
	}

	return req
}

// TestRDPScriptFloorNamesRDP: a `js` script over the floor because it calls
// rdp.connect is refused with the RDP reason, not the browser one — the
// floor it hit is 15 minutes, and "scripts that open a browser" would be a
// wrong explanation of it.
func TestRDPScriptFloorNamesRDP(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	rig := newRoundTripRig(t)

	script := `var s = rdp.connect({host: "rdp.acme.com", username: "u", password: secrets.P});
return { status: s.ok ? "up" : "down" };`

	_, err := rig.svc.CreateCheck(t.Context(), rig.org.Slug, jsCheckRequest("js-rdp-too-fast", script, "5m"))
	r.Error(err)
	r.Contains(err.Error(), "must be at least 15m")
	r.Contains(err.Error(), rdpFloorReasonText)
	r.NotContains(err.Error(), "open a browser", "the browser reason must not be blamed for the RDP floor")

	// A script that does both hits the higher (RDP) floor and names it.
	both := `var p = browser.open();
var s = rdp.connect({host: "rdp.acme.com", username: "u", password: secrets.P});
return { status: "up" };`

	_, err = rig.svc.CreateCheck(t.Context(), rig.org.Slug, jsCheckRequest("js-both-too-fast", both, "5m"))
	r.Error(err)
	r.Contains(err.Error(), rdpFloorReasonText)

	// Positive control: the same script at the floor is accepted.
	_, err = rig.svc.CreateCheck(t.Context(), rig.org.Slug, jsCheckRequest("js-rdp-ok", script, "15m"))
	r.NoError(err)
}

// TestRDPFloorHoldsWithEncryptionOn is the regression for the encryption
// bypass: with a master key set, the password is split out of the public
// config, and the update path validates the period against that public
// config. The floor must still hold — it keys on the username.
func TestRDPFloorHoldsWithEncryptionOn(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := setupEncryptedChecksService(t)
	ctx := t.Context()

	created, err := svc.CreateCheck(ctx, org.Slug, rdpCheckRequest("rdp-encrypted", "15m", true))
	r.NoError(err)

	// Non-vacuous: prove the password really left the public config, i.e. the
	// update below validates against a config that has a username and no
	// password.
	r.Contains(created.ConfigPrivateKeys, "password")

	row, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotContains(row.Config, "password")
	r.Equal("svc-monitor", row.Config["username"])

	fast := "1m"
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Period: &fast})
	r.Error(err, "a period-only PATCH must not drop an authenticated check below the floor")
	r.Contains(err.Error(), "must be at least 15m")
	r.Contains(err.Error(), rdpFloorReasonText)

	// Positive control: a period at the floor is accepted on the same row.
	ok := "30m"
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Period: &ok})
	r.NoError(err)
}

// TestRDPFloorHoldsOnConfigOnlyPatch: adding credentials to a pre-auth rdp
// check that already runs every minute turns it into a real logon every
// minute. A config-only PATCH must be refused just like a period PATCH.
func TestRDPFloorHoldsOnConfigOnlyPatch(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	rig := newRoundTripRig(t)

	created, err := rig.svc.CreateCheck(t.Context(), rig.org.Slug, rdpCheckRequest("rdp-preauth-fast", "1m", false))
	r.NoError(err, "the pre-auth check keeps the global floor")

	withCredentials := map[string]any{"host": "rdp.acme.com", "username": "svc-monitor", "password": "hunter2"}
	_, err = rig.svc.UpdateCheck(t.Context(), rig.org.Slug, created.UID, &checks.UpdateCheckRequest{
		Config: &withCredentials,
	})
	r.Error(err)
	r.Contains(err.Error(), rdpFloorReasonText)

	// Positive control: the same credentials with a compliant period go through.
	period := "15m"
	_, err = rig.svc.UpdateCheck(t.Context(), rig.org.Slug, created.UID, &checks.UpdateCheckRequest{
		Config: &withCredentials,
		Period: &period,
	})
	r.NoError(err)
}
