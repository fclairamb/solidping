package statusupdates

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestToResponse_ThreadsPublicationUID pins the thread pointer to the wire.
// Until spec 2026-09-16-14 the field existed on the model and never left the
// server, so the Updates & notices list had no way to tell a post threaded
// under a published incident from a standalone maintenance notice.
func TestToResponse_ThreadsPublicationUID(t *testing.T) {
	t.Parallel()

	pubUID := "pub-uid-1"
	update := &models.StatusUpdate{
		UID:                    "update-uid-1",
		StatusPageUID:          "page-uid-1",
		IncidentPublicationUID: &pubUID,
		Title:                  "Investigating elevated error rates",
		Kind:                   models.StatusUpdateKindInvestigating,
	}

	got := toResponse(update)

	require.NotNil(t, got.IncidentPublicationUID)
	require.Equal(t, pubUID, *got.IncidentPublicationUID)
}

// TestToResponse_StandaloneHasNoPublicationUID is the negative control: a
// maintenance notice threads under nothing, and a non-nil pointer here would
// make the list render a link to a publication that does not exist.
func TestToResponse_StandaloneHasNoPublicationUID(t *testing.T) {
	t.Parallel()

	update := &models.StatusUpdate{
		UID:           "update-uid-2",
		StatusPageUID: "page-uid-1",
		Title:         "Planned database maintenance",
		Kind:          models.StatusUpdateKindMaintenance,
	}

	got := toResponse(update)

	require.Nil(t, got.IncidentPublicationUID)
}
