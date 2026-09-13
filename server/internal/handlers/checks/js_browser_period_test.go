package checks_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// The two scripts the tests below turn on: one drives a browser, one does not.
// They differ ONLY in the browser call, so a difference in outcome can only
// come from the hint.
const (
	browserScript = `var page = browser.open();
var nav = page.goto(env.BASE_URL);
return { status: nav.ok ? "up" : "down" };
`
	plainScript = `var resp = http.get(env.BASE_URL);
return { status: resp.statusCode === 200 ? "up" : "down" };
`
)

func jsCheckRequest(slug, script, period string) checks.CreateCheckRequest {
	req := checks.CreateCheckRequest{
		Name: slug,
		Slug: slug,
		Type: "js",
		Config: map[string]any{
			"script": script,
			"env":    map[string]any{"BASE_URL": "https://acme.com"},
		},
	}

	if period != "" {
		req.Period = &period
	}

	return req
}

// TestBrowserScriptIsRejectedBelowTheBrowserFloor is spec 2026-09-12-06 §7's
// headline: a `js` check whose script opens a browser inherits the browser
// check's 1m floor, and the refusal SAYS WHY — the type's own documented floor
// is 30s, so a bare "must be at least 60s" would read as a bug.
func TestBrowserScriptIsRejectedBelowTheBrowserFloor(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	rig := newRoundTripRig(t)

	_, err := rig.svc.CreateCheck(t.Context(), rig.org.Slug,
		jsCheckRequest("js-browser-too-fast", browserScript, "30s"))

	r.Error(err)
	r.Contains(err.Error(), "period for js checks must be at least 60s")
	r.Contains(err.Error(), "scripts that open a browser have the browser check's 1m floor")

	// A PATCH must not be a way around the create-time rule.
	created, err := rig.svc.CreateCheck(t.Context(), rig.org.Slug,
		jsCheckRequest("js-browser-patch", browserScript, "1m"))
	r.NoError(err)

	fast := "30s"
	_, err = rig.svc.UpdateCheck(t.Context(), rig.org.Slug, *created.Slug, &checks.UpdateCheckRequest{
		Period: &fast,
	})
	r.Error(err)
	r.Contains(err.Error(), "scripts that open a browser have the browser check's 1m floor")

	// And neither is patching the SCRIPT onto a check already running at 30s.
	plain, err := rig.svc.CreateCheck(t.Context(), rig.org.Slug,
		jsCheckRequest("js-plain-then-browser", plainScript, "30s"))
	r.NoError(err)

	stillFast := "30s"
	_, err = rig.svc.UpdateCheck(t.Context(), rig.org.Slug, *plain.Slug, &checks.UpdateCheckRequest{
		Period: &stillFast,
		Config: &map[string]any{
			"script": browserScript,
			"env":    map[string]any{"BASE_URL": "https://acme.com"},
		},
	})
	r.Error(err)
	r.Contains(err.Error(), "scripts that open a browser have the browser check's 1m floor")
}

// TestBrowserScriptIsAcceptedAtTheBrowserFloor is the positive control: the
// same script at 1m is fine, so the rejection above is about the floor and not
// about a script the validator refuses outright.
func TestBrowserScriptIsAcceptedAtTheBrowserFloor(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	rig := newRoundTripRig(t)

	created, err := rig.svc.CreateCheck(t.Context(), rig.org.Slug,
		jsCheckRequest("js-browser-ok", browserScript, "1m"))

	r.NoError(err)
	r.Equal("00:01:00", *created.Period)
}

// TestScriptWithoutBrowserKeepsTheJSFloor: the hint must not raise the floor
// for every `js` check. A script that never opens a browser still runs at 30s.
func TestScriptWithoutBrowserKeepsTheJSFloor(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	rig := newRoundTripRig(t)

	created, err := rig.svc.CreateCheck(t.Context(), rig.org.Slug,
		jsCheckRequest("js-plain-fast", plainScript, "30s"))

	r.NoError(err)
	r.Equal("00:00:30", *created.Period)

	// Below the type's OWN floor is still refused, with the plain message and
	// no browser reason attached.
	_, err = rig.svc.CreateCheck(t.Context(), rig.org.Slug,
		jsCheckRequest("js-plain-too-fast", plainScript, "10s"))
	r.Error(err)
	r.Equal("period for js checks must be at least 30s", err.Error())
}

// TestNoPeriodBrowserScriptStoresTheBrowserFloor pins the create-without-period
// path: `js`'s DefaultPeriod is already 1m, equal to the browser floor, so a
// browser script created with no period stores a period the server would
// itself accept. This is the test that fails the day someone lowers the `js`
// default to 30s.
func TestNoPeriodBrowserScriptStoresTheBrowserFloor(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	rig := newRoundTripRig(t)

	created, err := rig.svc.CreateCheck(t.Context(), rig.org.Slug,
		jsCheckRequest("js-browser-default", browserScript, ""))

	r.NoError(err)
	r.Equal("00:01:00", *created.Period)

	stored, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, *created.Slug)
	r.NoError(err)
	r.Equal(time.Minute, time.Duration(stored.Period),
		"a browser script created with no period must store at least the browser floor")
}

// TestValidateEndpointReportsTheBrowserFloor: the dry-run validate endpoint
// must report the same refusal the write path would, or the dashboard would
// show a clean form and then fail on submit.
func TestValidateEndpointReportsTheBrowserFloor(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	rig := newRoundTripRig(t)

	resp, err := rig.svc.ValidateCheck(t.Context(), rig.org.Slug, &checks.ValidateCheckRequest{
		Type:   "js",
		Period: "30s",
		Config: map[string]any{"script": browserScript},
	})
	r.NoError(err)

	var found bool

	for _, finding := range resp.Fields {
		if finding.Name == "period" {
			found = true

			r.Contains(finding.Message, "scripts that open a browser have the browser check's 1m floor")
		}
	}

	r.False(resp.Valid)
	r.True(found, "the validate endpoint must report the period floor, got %#v", resp.Fields)
}
