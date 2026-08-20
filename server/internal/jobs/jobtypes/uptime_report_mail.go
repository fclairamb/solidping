package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/uptimereport"
)

// errJobsUnavailable is returned when the job queue is not wired (tests, or a
// process running without the jobs service).
var errJobsUnavailable = errors.New("jobs service unavailable")

// EnqueueUptimeReportEmail queues one rendered uptime report for one recipient.
//
// Exported so the "send me a test report" endpoint and the scheduled job share
// one enqueue path: a test send that took a different route could differ from
// the real mail in exactly the ways a test is supposed to catch.
//
// One job per recipient (never a single job with every address in To:) —
// recipients are PII, and a shared To: header would disclose the whole
// distribution list to each of them.
func EnqueueUptimeReportEmail(
	ctx context.Context, registry *services.Registry, orgUID, recipient string,
	data *uptimereport.Data, unsubscribeURL string,
) error {
	if registry == nil || registry.Jobs == nil {
		return errJobsUnavailable
	}

	// Copy so one address's unsubscribe URL can never end up in another's
	// email — the view model is shared across the whole fan-out.
	payload := *data
	payload.UnsubscribeURL = unsubscribeURL

	cfg := EmailJobConfig{
		To:           []string{recipient},
		Template:     uptimereport.TemplateName,
		TemplateData: payload,
		// A digest is bulk mail: RFC 2369 / RFC 8058 headers are mandatory,
		// not decorative. The in-body link alone leaves a recipient whose
		// client only offers the header-level control with no way out.
		ListUnsubscribeURL: unsubscribeURL,
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal uptime report email: %w", err)
	}

	if _, err := registry.Jobs.CreateJob(ctx, orgUID, string(jobdef.JobTypeEmail), raw, nil); err != nil {
		return fmt.Errorf("enqueue uptime report email: %w", err)
	}

	return nil
}
