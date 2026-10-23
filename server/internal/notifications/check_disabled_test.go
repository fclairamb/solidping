package notifications

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestResolvedTagCheckDisabled(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	disabled := models.ResolutionTypeCheckDisabled
	incident := &models.Incident{ResolutionType: &disabled}

	r.True(ResolvedByCheckDisable(incident))
	r.False(ResolvedByCheckDeletion(incident))
	r.Equal(checkDisabledTag, resolvedTag(incident))
	r.Equal(checkDisabledSentence, closedSentence(incident))
	r.Equal("disabled", closedKind(incident))

	auto := models.ResolutionTypeAuto
	r.Equal(recoveredTag, resolvedTag(&models.Incident{ResolutionType: &auto}))
}
