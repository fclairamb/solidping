package checkworker

import (
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/secretref/mask"
)

// maskErrorText returns the text of an error result with every resolved secret
// reference value of the job masked (see mask.ErrorText).
func maskErrorText(checkJob *models.CheckJob, text string) string {
	return mask.ErrorText(checkJob.ResolvedRefValues, text)
}

// maskResolvedValues returns output with every resolved secret reference value
// of the job masked, in string values and map keys at any depth (see
// mask.Output).
func maskResolvedValues(checkJob *models.CheckJob, output map[string]any) map[string]any {
	return mask.Output(checkJob.ResolvedRefValues, output, checkJob.CheckUID)
}
