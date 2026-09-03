package jobtypes

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
)

// OperatorNoticeJobDefinition is the factory for operator notices.
type OperatorNoticeJobDefinition struct{}

// Type returns the operator notice job type.
func (d *OperatorNoticeJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeOperatorNotice
}

// OperatorNoticeJobConfig is the wire form of one opsnotify.Notice.
//
// It is a separate struct from opsnotify.Notice on purpose: this one is
// PERSISTED in `jobs.config` and its JSON tags are therefore a compatibility
// contract with rows already in the queue, while the in-memory Notice is free
// to change shape.
type OperatorNoticeJobConfig struct {
	Event   string `json:"event"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	URL     string `json:"url,omitempty"`
}

// Notice converts the persisted config back into a deliverable notice.
func (c OperatorNoticeJobConfig) Notice() opsnotify.Notice {
	return opsnotify.Notice{Event: c.Event, Subject: c.Subject, Body: c.Body, URL: c.URL}
}

// CreateJobRun builds an executable instance.
//
//nolint:ireturn // Factory pattern requires interface return
func (d *OperatorNoticeJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	cfg := OperatorNoticeJobConfig{}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, fmt.Errorf("parsing operator notice config: %w", err)
		}
	}

	if cfg.Event == "" {
		return nil, errOperatorNoticeNoEvent
	}

	return &OperatorNoticeJobRun{cfg: cfg}, nil
}

// errOperatorNoticeNoEvent is returned for a notice with no event. Without one
// there is no subscription to match and no metric label to count under, so the
// job would be an expensive no-op.
var errOperatorNoticeNoEvent = fmt.Errorf("operator notice requires an event")

// OperatorNoticeJobRun is the runtime state for one notice delivery.
type OperatorNoticeJobRun struct {
	cfg OperatorNoticeJobConfig
}

// Run delivers the notice to every subscribed super admin.
//
// It is deliberately fail-soft in the same way the watchdog is: a recipient
// who cannot be reached is named in the log and counted, and the run still
// succeeds. Returning an error here would retry the whole fan-out and
// re-deliver to the recipients that already got it.
func (r *OperatorNoticeJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger.With("event", r.cfg.Event)

	cfg, err := opsnotify.LoadConfig(ctx, jctx.DBService)
	if err != nil {
		log.ErrorContext(ctx, "Failed to load the operator notifications configuration", "error", err)

		return jobdef.NewRetryableError(fmt.Errorf("load operator notifications config: %w", err))
	}

	if !cfg.Enabled {
		// Disabled means no delivery at all. The event itself was already
		// recorded by whatever raised it.
		log.DebugContext(ctx, "Operator notifications are disabled; skipping delivery")

		return nil
	}

	recipients := opsnotify.ResolveRecipients(ctx, jctx.DBService, log, cfg, r.cfg.Event)
	if len(recipients) == 0 {
		// Enabled with nobody eligible to tell. Same posture as the watchdog:
		// an operator must not be able to believe they are covered when the
		// only subscriber lost super_admin last month.
		log.WarnContext(ctx,
			"Operator notifications are enabled but this event has no eligible super-admin recipient; "+
				"the notice was logged, not delivered",
			"subject", r.cfg.Subject)

		return nil
	}

	deps := operatorNoticeDeps(jctx)
	notice := r.cfg.Notice()

	for _, userUID := range recipients {
		opsnotify.DeliverToUser(ctx, deps, log, userUID, notice)
	}

	return nil
}
