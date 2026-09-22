package notifications_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/notifications"
)

// degradedIncident builds a degraded incident carrying the details the evaluator
// writes, round-tripped through the float64/string shapes JSONB gives back.
func degradedIncident(details models.JSONMap) *models.Incident {
	incident := models.NewIncident("org", "check", time.Now(), "acme is degraded")
	incident.Kind = models.IncidentKindDegraded
	incident.Details = details

	return incident
}

func TestDegradedInfoForIgnoresOtherKinds(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	outage := models.NewIncident("org", "check", time.Now(), "acme is down")
	r.Nil(notifications.DegradedInfoFor(outage))

	burn := models.NewIncident("org", "check", time.Now(), "fast burn")
	burn.Kind = models.IncidentKindSLOBurn
	r.Nil(notifications.DegradedInfoFor(burn))
}

// TestDegradedWordingIsNotAnOutage is the wording requirement as a test: the
// spec's own reason for it is that a degraded notice which reads like an outage
// gets muted, and then the real outages are muted too.
func TestDegradedWordingIsNotAnOutage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// float64 everywhere, as a JSONB round-trip delivers it.
	info := notifications.DegradedInfoFor(degradedIncident(models.JSONMap{
		"degraded_failures_fired":   true,
		"degraded_failure_count":    float64(7),
		"degraded_failure_slots":    float64(60),
		"degraded_failures":         float64(5),
		"degraded_failures_window":  float64(60),
		"degraded_availability_pct": 93.8,
		"degraded_currently_up":     true,
	}))
	r.NotNil(info)

	headline := info.Headline("acme.com")

	r.Equal("acme.com is degraded: 7 failures in the last 60 probes (93.8%)", headline)
	r.NotContains(strings.ToLower(headline), "is down")
	r.NotContains(strings.ToLower(headline), "incident")
	r.Equal("Currently up.", info.StatusText())
	r.Contains(info.SummaryLines(), "Currently up.")
}

func TestDegradedWordingForTheSlowRule(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	info := notifications.DegradedInfoFor(degradedIncident(models.JSONMap{
		"degraded_slow_fired":   true,
		"degraded_slow_count":   float64(3),
		"degraded_slow_slots":   float64(6),
		"degraded_slow":         float64(3),
		"degraded_slow_window":  float64(6),
		"slow_threshold_ms":     float64(1000),
		"degraded_currently_up": true,
	}))
	r.NotNil(info)

	r.Equal("acme.com is degraded: 3 of the last 6 probes were slower than 1000ms",
		info.Headline("acme.com"))
}

// TestDegradedDeepLinkTargetsTheWindow pins the deep link: a reader who lands on
// a default 24 h view sees seven failures as seven pixels, which is exactly the
// "isolated red dots do not read as an event" problem the link exists to solve.
func TestDegradedDeepLinkTargetsTheWindow(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	from := time.Date(2026, 9, 22, 14, 35, 0, 0, time.UTC)
	to := time.Date(2026, 9, 22, 15, 28, 0, 0, time.UTC)

	info := notifications.DegradedInfoFor(degradedIncident(models.JSONMap{
		"degraded_failures_fired": true,
		"degraded_window_from":    from.Format(time.RFC3339),
		"degraded_window_to":      to.Format(time.RFC3339),
	}))
	r.NotNil(info)

	check := models.NewCheck("org", "acme", "http")

	url := notifications.DegradedCheckWindowURL("https://solidping.io", "acme-org", check, info)

	r.Contains(url, "/orgs/acme-org/checks/"+check.UID)
	r.Contains(url, "graphFrom="+strconv.FormatInt(from.UnixMilli(), 10))
	r.Contains(url, "graphTo="+strconv.FormatInt(to.UnixMilli(), 10))

	// With no window recorded the link degrades to the plain check page rather
	// than to a malformed one.
	bare := notifications.DegradedInfoFor(degradedIncident(models.JSONMap{"degraded_failures_fired": true}))
	plain := notifications.DegradedCheckWindowURL("https://solidping.io", "acme-org", check, bare)
	r.NotContains(plain, "graphFrom")
}
