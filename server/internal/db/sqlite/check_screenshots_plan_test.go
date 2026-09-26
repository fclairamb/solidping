package sqlite

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestCheckScreenshotListingUsesIndex_SQLite is the plan regression for spec
// 2026-09-25-34: the per-check screenshot listing must be answered from
// files_org_check_uid_idx, never by walking every attachment of the org.
//
// The EXPLAINed statement is the one ListCheckScreenshotFiles runs (same
// builder). The positive control spells the checkUid expression differently
// but equivalently — SQLite only matches an expression index on the verbatim
// expression — and must NOT use the index, which proves the assertion is about
// the index and the spelling rather than about a fixture too small to matter.
func TestCheckScreenshotListingUsesIndex_SQLite(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	org := models.NewOrganization("shotplan", "Shot Plan")
	r.NoError(s.CreateOrganization(ctx, org))

	checkUIDs := make([]string, 0, 40)

	for range 40 {
		checkUID := uuid.New().String()
		checkUIDs = append(checkUIDs, checkUID)

		for j := range 25 {
			topic := fmt.Sprintf("incidents/%s/screenshot", uuid.New().String())
			file := models.NewFile(org.UID, "shot.png", "image/png", "file://x", 10, nil)
			file.Topic = &topic
			file.Details = models.JSONMap{"checkUid": checkUID}
			file.CreatedAt = time.Now().Add(-time.Duration(j) * time.Minute)
			r.NoError(s.CreateFile(ctx, file))
		}
	}

	_, err = s.DB().ExecContext(ctx, "ANALYZE")
	r.NoError(err)

	var files []*models.File

	plan := explainSQLiteQuery(ctx, t, s, s.checkScreenshotFilesQuery(&files, org.UID, checkUIDs[0], 5).String())
	r.Contains(plan, "files_org_check_uid_idx",
		"the listing must ride the per-check expression index:\n%s", plan)

	control := strings.Replace(
		s.checkScreenshotFilesQuery(&files, org.UID, checkUIDs[0], 5).String(),
		"json_extract(details, '$.checkUid')", `json_extract(details, '$."checkUid"')`, 1)
	r.NotContains(control, "json_extract(details, '$.checkUid')", "the control must actually respell it")

	controlPlan := explainSQLiteQuery(ctx, t, s, control)
	r.NotContains(controlPlan, "files_org_check_uid_idx",
		"control: an equivalent but respelled expression cannot use the index — otherwise the "+
			"assertion above proves nothing:\n%s", controlPlan)

	// And the query still answers correctly.
	got, err := s.ListCheckScreenshotFiles(ctx, org.UID, checkUIDs[0], 5)
	r.NoError(err)
	r.Len(got, 5)

	for i := 1; i < len(got); i++ {
		r.False(got[i].CreatedAt.After(got[i-1].CreatedAt), "newest first")
	}
}
