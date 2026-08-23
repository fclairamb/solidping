package jobtypes

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// alertCustomDomainDemoted tells the organization that one of its status pages
// has stopped being served on its custom domain.
//
// Two deliveries, on purpose:
//
//   - an audit event, so the fact is in the org's activity feed and reaches its
//     event webhooks — the same shape statuspage.subscriber.disabled uses for
//     the same class of silent failure;
//   - an email to every owner/admin, because an audit row nobody is looking at
//     is not an alert. This is the org's existing operator-mail path
//     (JobTypeEmail), not a bespoke sender.
//
// It deliberately does NOT ride the incident/on-call fan-out: an incident
// requires a check (incidents.check_uid is NOT NULL) and a status page has no
// anchor check, so that route would mean inventing one.
//
// Every failure here is logged and swallowed. The demotion itself has already
// been written; failing the sweep because a mail could not be queued would
// leave the rest of the installation's domains unchecked.
func alertCustomDomainDemoted(
	ctx context.Context, jctx *jobdef.JobContext,
	page *models.StatusPage, failures int, diagnostic string,
) {
	domain := ""
	if page.CustomDomain != nil {
		domain = *page.CustomDomain
	}

	recordCustomDomainDemotedEvent(ctx, jctx, page, domain, failures, diagnostic)
	mailCustomDomainDemoted(ctx, jctx, page, domain, diagnostic)
}

// recordCustomDomainDemotedEvent writes the audit row.
func recordCustomDomainDemotedEvent(
	ctx context.Context, jctx *jobdef.JobContext,
	page *models.StatusPage, domain string, failures int, diagnostic string,
) {
	if jctx.DBService == nil {
		return
	}

	event := models.NewEvent(
		page.OrganizationUID, models.EventTypeStatusPageCustomDomainDemoted, models.ActorTypeSystem,
	)
	event.Payload = models.JSONMap{
		"statusPageUid":  page.UID,
		"statusPageSlug": page.Slug,
		"domain":         domain,
		"failures":       failures,
		"diagnostic":     diagnostic,
	}

	if err := jctx.DBService.CreateEvent(ctx, event); err != nil {
		jctx.Logger.ErrorContext(ctx, "Failed to record the custom-domain demotion event",
			"status_page_uid", page.UID, "domain", domain, "error", err)
	}
}

// mailCustomDomainDemoted queues one email per owner/admin of the org.
func mailCustomDomainDemoted(
	ctx context.Context, jctx *jobdef.JobContext,
	page *models.StatusPage, domain, diagnostic string,
) {
	if jctx.DBService == nil || jctx.Services == nil || jctx.Services.Jobs == nil {
		return
	}

	org, err := jctx.DBService.GetOrganization(ctx, page.OrganizationUID)
	if err != nil || org == nil {
		jctx.Logger.WarnContext(ctx, "Custom-domain demotion alert: cannot load the organization",
			"status_page_uid", page.UID, "error", err)

		return
	}

	members, err := jctx.DBService.ListMembersByOrg(ctx, page.OrganizationUID)
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Custom-domain demotion alert: cannot list the organization's admins",
			"status_page_uid", page.UID, "error", err)

		return
	}

	settingsURL := ""
	if jctx.AppConfig != nil {
		settingsURL = fmt.Sprintf("%s/dash0/orgs/%s/status-pages/%s",
			jctx.AppConfig.Server.BaseURL, org.Slug, page.UID)
	}

	data := map[string]any{
		"OrgName":        org.Name,
		"StatusPageName": page.Name,
		"Domain":         domain,
		"Diagnostic":     diagnostic,
		"SettingsURL":    settingsURL,
	}

	for _, member := range members {
		if !member.Role.AtLeast(models.MemberRoleAdmin) || member.User == nil || member.User.Email == "" {
			continue
		}

		enqueueCustomDomainEmail(ctx, jctx, page.OrganizationUID, member.User.Email, data)
	}
}

// enqueueCustomDomainEmail queues one templated email job.
func enqueueCustomDomainEmail(
	ctx context.Context, jctx *jobdef.JobContext, orgUID, recipient string, data map[string]any,
) {
	raw, err := json.Marshal(EmailJobConfig{
		To:       []string{recipient},
		Template: email.TemplateCustomDomainDemoted,
		// Transactional operator mail: no List-Unsubscribe. An admin cannot opt
		// out of being told their status page went dark.
		TemplateData: data,
	})
	if err != nil {
		jctx.Logger.ErrorContext(ctx, "Failed to marshal the custom-domain demotion email", "error", err)

		return
	}

	if _, err := jctx.Services.Jobs.CreateJob(ctx, orgUID, string(jobdef.JobTypeEmail), raw, nil); err != nil {
		jctx.Logger.ErrorContext(ctx, "Failed to enqueue the custom-domain demotion email", "error", err)
	}
}
