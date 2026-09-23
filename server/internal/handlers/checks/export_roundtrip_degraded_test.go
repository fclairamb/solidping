package checks_test

// The export/import round trip for degraded-detection configuration (spec
// 2026-09-23-01). The bug this guards against: ExportCheck never carried the
// six degraded fields, so a customized check silently reverted to the code
// defaults on export/import, and — the mirror-image failure a naive fix could
// reintroduce — exporting the EFFECTIVE (resolved) values instead of the raw
// pointers would freeze today's defaults into every unconfigured check's
// document.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// TestDegradedConfigRoundTripsThroughExportImport is the positive half: a
// check customized away from every default (including an explicit
// DegradedEnabled=false, which must survive as an explicit false, not
// collapse to "unset") round-trips its RAW DB columns unchanged through a real
// export -> import.
func TestDegradedConfigRoundTripsThroughExportImport(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newRoundTripRig(t)

	r.True(rig.create(t, checks.CreateCheckRequest{
		Name: "Customized", Slug: "customized", Type: "http",
		Config:                 map[string]any{"url": "https://acme.com/customized"},
		DegradedFailures:       intPtr(10),
		DegradedFailuresWindow: intPtr(80),
		DegradedSlow:           intPtr(4),
		DegradedSlowWindow:     intPtr(9),
		SlowThresholdMs:        intPtr(1500),
		DegradedEnabled:        boolPtr(false),
	}))

	before, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, "customized")
	r.NoError(err)
	r.Equal(10, *before.DegradedFailures)
	r.Equal(80, *before.DegradedFailuresWindow)
	r.Equal(4, *before.DegradedSlow)
	r.Equal(9, *before.DegradedSlowWindow)
	r.Equal(1500, *before.SlowThresholdMs)
	r.False(before.DegradedEnabled)

	doc, err := rig.svc.ExportChecks(t.Context(), rig.org.Slug, checks.ListChecksOptions{})
	r.NoError(err)
	r.Len(doc.Checks, 1)

	exported := doc.Checks[0]
	r.NotNil(exported.DegradedFailures)
	r.Equal(10, *exported.DegradedFailures)
	r.NotNil(exported.DegradedFailuresWindow)
	r.Equal(80, *exported.DegradedFailuresWindow)
	r.NotNil(exported.DegradedSlow)
	r.Equal(4, *exported.DegradedSlow)
	r.NotNil(exported.DegradedSlowWindow)
	r.Equal(9, *exported.DegradedSlowWindow)
	r.NotNil(exported.SlowThresholdMs)
	r.Equal(1500, *exported.SlowThresholdMs)
	r.False(exported.DegradedEnabled)

	// A REAL import (dryRun=false) of the org's own export, onto the SAME
	// check. This is exactly the shape a naive fix (exporting Effective*()
	// instead of the raw pointer) would still pass — the interesting
	// assertion is the companion test below.
	result, err := rig.svc.ImportChecks(t.Context(), rig.org.Slug, doc, false)
	r.NoError(err)
	r.Empty(result.Errors, "%+v", result.Errors)

	after, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, "customized")
	r.NoError(err)
	r.NotNil(after.DegradedFailures)
	r.Equal(10, *after.DegradedFailures, "the round-tripped raw column must match the original customization")
	r.NotNil(after.DegradedFailuresWindow)
	r.Equal(80, *after.DegradedFailuresWindow)
	r.NotNil(after.DegradedSlow)
	r.Equal(4, *after.DegradedSlow)
	r.NotNil(after.DegradedSlowWindow)
	r.Equal(9, *after.DegradedSlowWindow)
	r.NotNil(after.SlowThresholdMs)
	r.Equal(1500, *after.SlowThresholdMs)
	r.False(after.DegradedEnabled, "an explicit opt-out must survive the round trip")
}

// TestUnconfiguredDegradedConfigStaysNullThroughExportImport is the
// regression this bug would otherwise silently reintroduce: a check that
// never customized its degraded configuration stores NULL in all five numeric
// columns, and a naive fix that exports check.EffectiveDegradedFailures() & co
// instead of the raw pointers would pass the test above while FAILING this
// one — every unconfigured check would come back from a round trip with the
// resolved defaults (5/60/3/6/0) frozen into its row, and any future retune of
// models.Default* would then silently stop reaching it.
func TestUnconfiguredDegradedConfigStaysNullThroughExportImport(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newRoundTripRig(t)

	r.True(rig.create(t, checks.CreateCheckRequest{
		Name: "Plain", Slug: "plain", Type: "http",
		Config: map[string]any{"url": "https://acme.com/plain"},
	}))

	before, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, "plain")
	r.NoError(err)
	r.Nil(before.DegradedFailures)
	r.Nil(before.DegradedFailuresWindow)
	r.Nil(before.DegradedSlow)
	r.Nil(before.DegradedSlowWindow)
	r.Nil(before.SlowThresholdMs)

	doc, err := rig.svc.ExportChecks(t.Context(), rig.org.Slug, checks.ListChecksOptions{})
	r.NoError(err)
	r.Len(doc.Checks, 1)

	exported := doc.Checks[0]

	// (3) The export document itself must omit the degraded fields entirely
	// for an unconfigured check — raw pointers are nil, and omitempty drops
	// them from the JSON — not merely leave the DB NULL after import.
	r.Nil(exported.DegradedFailures, "an unconfigured check's export must carry no degradedFailures value")
	r.Nil(exported.DegradedFailuresWindow)
	r.Nil(exported.DegradedSlow)
	r.Nil(exported.DegradedSlowWindow)
	r.Nil(exported.SlowThresholdMs)

	rendered, err := checks.MarshalExportDocument(doc)
	r.NoError(err)

	var generic struct {
		Checks []map[string]any `json:"checks"`
	}
	r.NoError(json.Unmarshal(rendered, &generic))
	r.Len(generic.Checks, 1)

	for _, key := range []string{
		"degradedFailures", "degradedFailuresWindow", "degradedSlow", "degradedSlowWindow", "slowThresholdMs",
	} {
		r.NotContains(generic.Checks[0], key,
			"the rendered export document must omit %q for an unconfigured check", key)
	}

	// A REAL import (dryRun=false) of the org's own export, onto the SAME
	// check. This is the assertion a naive Effective*()-based fix fails: it
	// would write 5/60/3/6/0 here instead of leaving the columns NULL.
	result, err := rig.svc.ImportChecks(t.Context(), rig.org.Slug, doc, false)
	r.NoError(err)
	r.Empty(result.Errors, "%+v", result.Errors)

	after, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, "plain")
	r.NoError(err)
	r.Nil(after.DegradedFailures, "re-importing an unconfigured check must not write a resolved default")
	r.Nil(after.DegradedFailuresWindow)
	r.Nil(after.DegradedSlow)
	r.Nil(after.DegradedSlowWindow)
	r.Nil(after.SlowThresholdMs)

	r.Equal(5, after.EffectiveDegradedFailures(), "the code default still resolves at read time")
	r.Equal(60, after.EffectiveDegradedFailuresWindow())
	r.Equal(3, after.EffectiveDegradedSlow())
	r.Equal(6, after.EffectiveDegradedSlowWindow())
	r.Equal(0, after.EffectiveSlowThresholdMs())
}
