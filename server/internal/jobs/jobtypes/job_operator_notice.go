package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
	// AboutUserUID names the user the notice is about (a new signup). The org
	// they landed in is resolved HERE rather than by the raiser, because on
	// every signup path the `users` row is inserted before the membership.
	AboutUserUID string `json:"aboutUserUid,omitempty"`
}

// NewOperatorNoticeJobConfig is the ONLY place a Notice becomes a queued job
// payload.
//
// It exists because the field-by-field copy used to live inline in the
// dispatcher wiring, where it silently dropped AboutUserUID and killed the
// landing-organization line in production while every test — which built the
// job config by hand on one side and asserted at the Notify boundary on the
// other — stayed green. One conversion, next to the struct, with a round-trip
// test that fails if a field stops traveling.
func NewOperatorNoticeJobConfig(notice *opsnotify.Notice) OperatorNoticeJobConfig {
	return OperatorNoticeJobConfig{
		Event:        notice.Event,
		Subject:      notice.Subject,
		Body:         notice.Body,
		URL:          notice.URL,
		AboutUserUID: notice.AboutUserUID,
	}
}

// Notice converts the persisted config back into a deliverable notice.
func (c *OperatorNoticeJobConfig) Notice() *opsnotify.Notice {
	return &opsnotify.Notice{
		Event:        c.Event,
		Subject:      c.Subject,
		Body:         c.Body,
		URL:          c.URL,
		AboutUserUID: c.AboutUserUID,
	}
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
var errOperatorNoticeNoEvent = errors.New("operator notice requires an event")

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
	notice := enrichWithOrganization(ctx, jctx, r.cfg.Notice())

	for _, userUID := range recipients {
		opsnotify.DeliverToUser(ctx, deps, log, userUID, notice)
	}

	return nil
}

// enrichWithOrganization appends the landing organization to a notice about a
// user, and points the deep link at that org's member list.
//
// This runs at DELIVERY time on purpose. Every signup path — password
// confirmation, each OAuth/OIDC/SAML/LDAP find-or-create, invite acceptance —
// inserts the `users` row BEFORE the membership, so "which org did they land
// in?" is simply not answerable at the moment the notice is raised. One queue
// hop later it is. A user who genuinely joined none (the self-registration
// path that creates no org) is reported as such, which is exactly the stuck
// signup an operator wants to notice.
func enrichWithOrganization(
	ctx context.Context, jctx *jobdef.JobContext, notice *opsnotify.Notice,
) *opsnotify.Notice {
	if notice.AboutUserUID == "" || jctx.DBService == nil {
		return notice
	}

	members, err := jctx.DBService.ListMembersByUser(ctx, notice.AboutUserUID)
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Could not resolve the new user's organizations for the operator notice",
			"aboutUserUid", notice.AboutUserUID, "error", err)

		return notice
	}

	if len(members) == 0 {
		notice.Body += "\nOrg:    no organization"

		return notice
	}

	slugs := make([]string, 0, len(members))

	for _, member := range members {
		org, orgErr := jctx.DBService.GetOrganization(ctx, member.OrganizationUID)
		if orgErr != nil || org == nil {
			continue
		}

		slugs = append(slugs, org.Slug)
	}

	if len(slugs) == 0 {
		return notice
	}

	notice.Body += "\nOrg:    " + strings.Join(slugs, ", ")

	if notice.URL == "" && jctx.AppConfig != nil {
		notice.URL = strings.TrimRight(jctx.AppConfig.Server.BaseURL, "/") +
			"/dash0/orgs/" + slugs[0] + "/organization/members"
	}

	return notice
}
