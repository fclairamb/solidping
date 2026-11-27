package agentws

import (
	"context"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/secretref"
	"github.com/fclairamb/solidping/server/internal/secretref/mask"
)

// maskAgentResult masks, in what an agent reports, the values the job's
// `${param:…}` references resolve to (spec 2026-11-27-04). The masking runs in
// the process executing the check, and an agent predating v0.56.0 has none: it
// would store a resolved value wherever its checker echoed it (a `url.Parse`
// error, a final URL, a metric key). The server resolved those values itself
// for the claim, so it masks them again here. A current agent sent them masked
// already, and masking twice changes nothing.
//
// A job whose public config holds no reference costs one scan and is returned
// as sent. `${env:…}` values are resolved on the agent, unknown here, and stay
// the agent's responsibility. When the values cannot be recomputed (a
// parameter deleted since the claim) the result is stored as sent: dropping
// it would lose a probe outcome over a value that is no longer current.
func (h *Handler) maskAgentResult(
	ctx context.Context, job *models.CheckJob, frame *agentcrypto.ClientFrame,
) (map[string]any, map[string]any, *checkerdef.Diagnostics) {
	config := map[string]any(job.Config)
	if !secretref.ContainsInConfig(config) {
		return frame.Output, frame.Metrics, frame.Diagnostics
	}

	overlay, err := checkjobsvc.ParamOverlay(ctx, h.dbService, job.OrganizationUID, config)
	if err != nil {
		h.logger.WarnContext(ctx, "agent result stored unmasked: its parameter references no longer resolve",
			"error", err, "job_uid", job.UID, "check_uid", job.CheckUID)

		return frame.Output, frame.Metrics, frame.Diagnostics
	}

	if len(overlay) == 0 {
		return frame.Output, frame.Metrics, frame.Diagnostics
	}

	resolved := make(map[string]any, len(config))
	for key, value := range config {
		resolved[key] = value
	}

	for key, value := range overlay {
		resolved[key] = value
	}

	values := secretref.ReferenceValues(config, resolved)
	secrets := mask.Values(values)

	if len(secrets) == 0 {
		return frame.Output, frame.Metrics, frame.Diagnostics
	}

	output := frame.Output
	if text, ok := output[checkerdef.OutputKeyError].(string); ok {
		// The error text may quote a value Go-escaped (`%q`), as the agent's
		// own maskErrorText would have handled.
		if masked := mask.ErrorText(values, text); masked != text {
			output = make(map[string]any, len(frame.Output))
			for key, value := range frame.Output {
				output[key] = value
			}

			output[checkerdef.OutputKeyError] = masked
		}
	}

	return mask.Output(values, output, job.CheckUID),
		mask.MetricKeys(frame.Metrics, secrets),
		mask.Diagnostics(frame.Diagnostics, secrets)
}
