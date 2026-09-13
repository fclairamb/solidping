package checks_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// TestUpsertCreateResolvesTypeDefaultPeriod covers spec 2026-09-11-07's item
// 3: PUT-by-slug's create branch (UpsertCheck → CreateCheck when no existing
// row matches) must land the same type-aware default a direct POST /checks
// would, not NewCheck's flat 1-minute constant. dnsbl (floor 15m, default 1h)
// is used here so this exercises a DIFFERENT type from the headline test's
// primary coverage.
func TestUpsertCreateResolvesTypeDefaultPeriod(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newRoundTripRig(t)

	resp, wasCreate, err := rig.svc.UpsertCheck(t.Context(), rig.org.Slug, "dnsbl-upsert", &checks.UpsertCheckRequest{
		Name: "DNSBL upsert", Type: "dnsbl", Config: map[string]any{"target": "1.2.3.4"},
	})
	r.NoError(err)
	r.True(wasCreate, "a slug with no existing row must take the create branch")
	r.NotNil(resp.Period)

	var stored timeutils.Duration
	r.NoError(stored.Scan(*resp.Period))
	r.Equal(time.Hour, time.Duration(stored),
		"upsert-create must resolve dnsbl's own 1h default, not the flat 1m constant")
}

// TestImportCreateResolvesTypeDefaultPeriod covers spec 2026-09-11-07's item
// 3: an imported document that omits `period` for a new check must produce
// the type's default on the row it creates, exactly like a direct
// POST /checks with no period — buildImportUpsertRequest leaves Period nil
// when the document entry's Period is "", which lands on the very same
// CreateCheck path.
func TestImportCreateResolvesTypeDefaultPeriod(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newRoundTripRig(t)

	doc := importOne(rig.org.Slug, checks.ExportCheck{
		Name: "DNSBL import", Slug: "dnsbl-import", Type: "dnsbl", Enabled: true,
		Config: map[string]any{"target": "5.6.7.8"},
		// Period deliberately absent/empty.
	})

	result, err := rig.svc.ImportChecks(t.Context(), rig.org.Slug, doc, false)
	r.NoError(err)
	r.Empty(result.Errors, "%+v", result.Errors)
	r.Equal(1, result.Created)

	stored, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, "dnsbl-import")
	r.NoError(err)
	r.Equal(time.Hour, time.Duration(stored.Period),
		"an imported no-period dnsbl check must land its 1h default, not the flat 1m constant")
}

// TestCreateValidateAgreeOnNoPeriodEffectivePeriod is the create/validate
// parity assertion spec 2026-09-11-07 asks for, observed through the
// regionSpread bound exactly like spec 2026-08-28-14's own parity tests: a
// no-period ssl request's EFFECTIVE period (used to bound regionSpread) must
// be the same on both POST /checks and POST /checks/validate. Before this
// spec, CreateCheck's stored period (eventually 1m via NewCheck) and
// planCreateCheck/validateRequestFieldFindings's effectivePeriod (also 1m,
// since defaultCheckPeriod was the ONLY fallback) happened to agree — the fix
// changes what "agree" means (both must now resolve ssl's own 6h default) and
// this pins that they still do.
func TestCreateValidateAgreeOnNoPeriodEffectivePeriod(t *testing.T) {
	t.Parallel()

	sslConfig := map[string]any{"host": "example.com"}

	cases := []struct {
		name         string
		regionSpread string
		wantAccepted bool
	}{
		{
			// Below ssl's 6h default: accepted ONLY if both sides resolve the
			// no-period effective period to 6h rather than a flat 1m (which
			// this spread already exceeds).
			name: "regionSpread well under the 6h default is accepted", regionSpread: "5m", wantAccepted: true,
		},
		{
			// At-or-above the 6h default: refused by both.
			name: "regionSpread at or above the 6h default is refused", regionSpread: "6h", wantAccepted: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			router, orgSlug := newParityRouter(t)

			body := map[string]any{
				"type": "ssl", "config": sslConfig, "regionSpread": tc.regionSpread,
				"slug": "ssl-" + strings.ReplaceAll(strings.ToLower(tc.regionSpread), " ", "-"),
			}

			validateStatus, validateBody := postJSON(t, router, "/api/v1/orgs/"+orgSlug+"/checks/validate", body)
			r.Equal(http.StatusOK, validateStatus, "validate must always answer 200: %v", validateBody)

			createStatus, createBody := postJSON(t, router, "/api/v1/orgs/"+orgSlug+"/checks", body)

			valid, _ := validateBody["valid"].(bool)

			if tc.wantAccepted {
				r.True(valid, "validate should accept: %v", validateBody)
				r.Equal(http.StatusCreated, createStatus, "create should accept: %v", createBody)

				period, _ := createBody["period"].(string)
				var stored timeutils.Duration
				r.NoError(stored.Scan(period))
				r.Equal(6*time.Hour, time.Duration(stored), "create must store ssl's own 6h default")

				return
			}

			r.False(valid, "validate should refuse: %v", validateBody)
			fields, _ := validateBody["fields"].([]any)
			r.NotEmpty(fields, "valid:false must carry the blocking field: %v", validateBody)
			firstField, ok := fields[0].(map[string]any)
			r.True(ok, "fields[0] must be an object: %v", validateBody)
			r.Equal("regionSpread", firstField["name"], "validate fields[0].name: %v", validateBody)

			r.Equal(http.StatusBadRequest, createStatus, "create status: %v", createBody)
		})
	}
}
