package customdomain

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// AlertDeps is everything AlertDemoted needs. It is passed explicitly rather
// than taken from a job context because the alert has TWO callers that share no
// runtime: the periodic sweep (which has a *jobdef.JobContext) and the
// dashboard's synchronous Verify button (which has a handler service). A hard
// demotion reached by clicking Verify at failure 11 takes the page just as dark
// as one the sweep reaches on its own, so it must notify identically.
type AlertDeps struct {
	// DB writes the audit event. Required; without it nothing is recorded.
	DB db.Service
	// Jobs queues the operator email. Nil in tests and in wiring that has no
	// job service — the audit event is still written.
	Jobs jobsvc.Service
	// BaseURL builds the dashboard link in the email. Empty omits the link.
	BaseURL string
	// Logger receives the delivery failures. Defaults to slog.Default.
	Logger *slog.Logger
}

// emailJobConfig is the subset of jobtypes.EmailJobConfig this package writes.
// Declared here rather than imported because jobtypes imports this package —
// taking the type from there would close an import cycle. The two are held in
// step by the job's own unmarshal-time validation, which rejects a config
// missing recipients, subject or template.
type emailJobConfig struct {
	To           []string       `json:"to"`
	Template     string         `json:"template,omitempty"`
	TemplateData map[string]any `json:"templateData,omitempty"`
}

// AlertDemoted tells the organization that one of its status pages has stopped
// being served on its custom domain.
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
// been written; failing the caller because a mail could not be queued would
// abort a sweep over every OTHER domain in the installation, or turn a
// successful Verify into a 500.
func AlertDemoted(ctx context.Context, deps AlertDeps, page *models.StatusPage, failures int, diagnostic string) {
	if page == nil {
		return
	}

	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}

	domain := ""
	if page.CustomDomain != nil {
		domain = *page.CustomDomain
	}

	recordDemotedEvent(ctx, deps, page, domain, failures, diagnostic)
	mailDemoted(ctx, deps, page, domain, diagnostic)
}

// recordDemotedEvent writes the audit row.
func recordDemotedEvent(
	ctx context.Context, deps AlertDeps,
	page *models.StatusPage, domain string, failures int, diagnostic string,
) {
	if deps.DB == nil {
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

	if err := deps.DB.CreateEvent(ctx, event); err != nil {
		deps.Logger.ErrorContext(ctx, "Failed to record the custom-domain demotion event",
			"status_page_uid", page.UID, "domain", domain, "error", err)
	}
}

// mailDemoted queues one email per owner/admin of the org.
func mailDemoted(
	ctx context.Context, deps AlertDeps,
	page *models.StatusPage, domain, diagnostic string,
) {
	if deps.DB == nil || deps.Jobs == nil {
		return
	}

	org, err := deps.DB.GetOrganization(ctx, page.OrganizationUID)
	if err != nil || org == nil {
		deps.Logger.WarnContext(ctx, "Custom-domain demotion alert: cannot load the organization",
			"status_page_uid", page.UID, "error", err)

		return
	}

	members, err := deps.DB.ListMembersByOrg(ctx, page.OrganizationUID)
	if err != nil {
		deps.Logger.WarnContext(ctx, "Custom-domain demotion alert: cannot list the organization's admins",
			"status_page_uid", page.UID, "error", err)

		return
	}

	settingsURL := ""
	if deps.BaseURL != "" {
		settingsURL = fmt.Sprintf("%s/dash0/orgs/%s/status-pages/%s", deps.BaseURL, org.Slug, page.UID)
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

		enqueueDemotedEmail(ctx, deps, page.OrganizationUID, member.User.Email, data)
	}
}

// enqueueDemotedEmail queues one templated email job.
func enqueueDemotedEmail(
	ctx context.Context, deps AlertDeps, orgUID, recipient string, data map[string]any,
) {
	raw, err := json.Marshal(emailJobConfig{
		To:       []string{recipient},
		Template: email.TemplateCustomDomainDemoted,
		// Transactional operator mail: no List-Unsubscribe. An admin cannot opt
		// out of being told their status page went dark.
		TemplateData: data,
	})
	if err != nil {
		deps.Logger.ErrorContext(ctx, "Failed to marshal the custom-domain demotion email", "error", err)

		return
	}

	if _, err := deps.Jobs.CreateJob(ctx, orgUID, string(jobdef.JobTypeEmail), raw, nil); err != nil {
		deps.Logger.ErrorContext(ctx, "Failed to enqueue the custom-domain demotion email", "error", err)
	}
}
