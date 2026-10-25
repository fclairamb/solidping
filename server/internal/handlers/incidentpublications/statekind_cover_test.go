package incidentpublications

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestStateForKind(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal(models.PublicationStateInvestigating, stateForKind(models.StatusUpdateKindInvestigating))
	r.Equal(models.PublicationStateIdentified, stateForKind(models.StatusUpdateKindIdentified))
	r.Equal(models.PublicationStateMonitoring, stateForKind(models.StatusUpdateKindMonitoring))
	r.Equal(models.PublicationStateResolved, stateForKind(models.StatusUpdateKindResolved))
	r.Empty(stateForKind(models.StatusUpdateKindMaintenance))
	r.Empty(stateForKind(models.StatusUpdateKindInfo))
	r.Empty(stateForKind(models.StatusUpdateKind("other")))
}
