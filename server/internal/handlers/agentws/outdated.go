package agentws

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/i18n/recipient"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/version"
)

// Outdated private-location agent notice (spec 2026-11-26-03).
//
// An agent that predates the rolling upgrade is never asked to restart, and an
// agent installed from a moving tag does not move on restart either: the only
// signal was an amber badge on a page customers rarely open. When such an agent
// connects (or has stayed drifted for outdatedGrace), the org gets an
// agent.outdated event and its owners and admins one email, once per agent and
// per server version.

const (
	// outdatedGrace is how long a non-legacy agent may stay drifted before the
	// org is told: its automatic upgrade normally lands well inside it.
	outdatedGrace = 72 * time.Hour
	// outdatedRecheckInterval throttles the check on a long-lived connection
	// (claims can arrive several times a second).
	outdatedRecheckInterval = 10 * time.Minute
	// outdatedTimeout bounds the notice's database work.
	outdatedTimeout = 10 * time.Second

	outdatedImagePath = "/solidping:current"
	// auditTargetAgent is the event target type of an agent.
	auditTargetAgent = "agent"
)

// OutdatedNotifier carries what the outdated-agent notice needs beyond the
// handler's own database: the job queue for the email and the public base URL.
type OutdatedNotifier struct {
	Jobs    jobsvc.Service
	BaseURL string
}

// SetOutdatedNotifier installs the email side of the outdated-agent notice.
// Without it the event is still recorded and no email is sent.
func (h *Handler) SetOutdatedNotifier(notifier OutdatedNotifier) {
	h.outdated = notifier
}

// maybeNotifyOutdated runs the outdated-agent check for the connection, at most
// once per outdatedRecheckInterval. Best effort: every failure is logged.
func (h *Handler) maybeNotifyOutdated(ctx context.Context, state *connState, report versionReport) {
	now := time.Now()
	if !state.outdatedCheckedAt.IsZero() && now.Sub(state.outdatedCheckedAt) < outdatedRecheckInterval {
		return
	}

	if report.version == "" || state.agent.IsSystem() || state.agent.OrgUID() == "" {
		return
	}

	state.outdatedCheckedAt = now

	serverVersion := version.Get().Version
	if isDevBuild(serverVersion) || isDevBuild(report.version) {
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), outdatedTimeout)
	defer cancel()

	if report.version == serverVersion {
		// Back on the server's version: the next drift starts a fresh clock.
		h.updateWorker(ctx, state, "version_drifted_since = NULL", "version_drifted_since IS NOT NULL")

		return
	}

	if report.pinned || !h.graceElapsed(ctx, state, report, now) {
		return
	}

	// Claim the notice atomically: replicas racing on the same agent write one.
	claimed, err := h.dbService.DB().NewUpdate().Model((*models.Worker)(nil)).
		Set("outdated_notified_version = ?", serverVersion).
		Where("uid = ?", state.workerUID).
		Where("(outdated_notified_version IS NULL OR outdated_notified_version <> ?)", serverVersion).
		Exec(ctx)
	if err != nil {
		h.logger.WarnContext(ctx, "could not record the outdated-agent notice", "agent", state.agent.UID, "error", err)

		return
	}

	if n, rowsErr := claimed.RowsAffected(); rowsErr != nil || n == 0 {
		return
	}

	h.notifyOutdated(ctx, state.agent, report, serverVersion)
}

// updateWorker applies one best-effort SET to the connection's workers row.
func (h *Handler) updateWorker(ctx context.Context, state *connState, set, where string) {
	if _, err := h.dbService.DB().NewUpdate().Model((*models.Worker)(nil)).
		Set(set).Where("uid = ?", state.workerUID).Where(where).Exec(ctx); err != nil {
		h.logger.WarnContext(ctx, "could not update the agent's drift clock", "agent", state.agent.UID, "error", err)
	}
}

// graceElapsed starts the drift clock on the first drifted report and says
// whether the agent is due a notice: legacy agents at once, the others after
// outdatedGrace.
func (h *Handler) graceElapsed(ctx context.Context, state *connState, report versionReport, now time.Time) bool {
	db := h.dbService.DB()

	if _, err := db.NewUpdate().Model((*models.Worker)(nil)).
		Set("version_drifted_since = ?", now).
		Where("uid = ?", state.workerUID).Where("version_drifted_since IS NULL").
		Exec(ctx); err != nil {
		h.logger.WarnContext(ctx, "could not start the agent's drift clock", "agent", state.agent.UID, "error", err)

		return false
	}

	if report.legacy {
		return true
	}

	worker := new(models.Worker)
	if err := db.NewSelect().Model(worker).Where("uid = ?", state.workerUID).Scan(ctx); err != nil {
		h.logger.WarnContext(ctx, "could not read the agent's drift clock", "agent", state.agent.UID, "error", err)

		return false
	}

	return worker.VersionDriftedSince != nil && now.Sub(*worker.VersionDriftedSince) >= outdatedGrace
}

// notifyOutdated writes the agent.outdated event and mails the org's admins.
func (h *Handler) notifyOutdated(
	ctx context.Context, agent *models.Agent, report versionReport, serverVersion string,
) {
	orgUID := agent.OrgUID()

	audit.Record(ctx, h.dbService, orgUID, models.EventTypeAgentOutdated,
		audit.Target{Type: auditTargetAgent, UID: agent.UID, Name: agent.Name},
		models.JSONMap{
			"agent":          agent.Name,
			"version":        report.version,
			"server_version": serverVersion,
			"legacy":         report.legacy,
		})

	h.logger.InfoContext(ctx, "agent is outdated: its organization's admins were told",
		"agent", agent.UID, "agent_version", report.version, "server_version", serverVersion,
		"legacy", report.legacy)

	if h.outdated.Jobs == nil {
		return
	}

	org, err := h.dbService.GetOrganization(ctx, orgUID)
	if err != nil || org == nil {
		h.logger.WarnContext(ctx, "outdated-agent notice: cannot load the organization",
			"organization_uid", orgUID, "error", err)

		return
	}

	members, err := h.dbService.ListMembersByOrg(ctx, orgUID)
	if err != nil {
		h.logger.WarnContext(ctx, "outdated-agent notice: cannot list the organization's admins",
			"organization_uid", orgUID, "error", err)

		return
	}

	data := OutdatedEmailData(agent.Name, report.version, serverVersion, report.legacy,
		imageHost(h.outdated.BaseURL), privateLocationsURL(h.outdated.BaseURL, org.Slug))
	email.ApplyOrgBranding(data, org.Name, org.Slug, org.LogoURL)

	for _, member := range members {
		if !member.Role.AtLeast(models.MemberRoleAdmin) || member.User == nil ||
			!email.ValidAddress(member.User.Email) {
			continue
		}

		loc := recipient.ForUser(member.User)

		raw, marshalErr := json.Marshal(map[string]any{
			"to":           []string{member.User.Email},
			"template":     email.TemplateAgentOutdated,
			"templateData": data,
			"locale":       loc.Lang(),
			"timezone":     loc.Timezone(),
		})
		if marshalErr != nil {
			h.logger.ErrorContext(ctx, "could not marshal the outdated-agent email", "error", marshalErr)

			return
		}

		if _, jobErr := h.outdated.Jobs.CreateJob(ctx, orgUID, string(jobdef.JobTypeEmail), raw, nil); jobErr != nil {
			h.logger.ErrorContext(ctx, "could not enqueue the outdated-agent email",
				"organization_uid", orgUID, "error", jobErr)
		}
	}
}

// OutdatedEmailData is the agent-outdated.html view model, without the org
// branding. The email preview builds its fixture through it too.
func OutdatedEmailData(
	agentName, agentVersion, serverVersion string, legacy bool, imageHost, locationsURL string,
) map[string]any {
	return map[string]any{
		"AgentName":     agentName,
		"AgentVersion":  agentVersion,
		"ServerVersion": serverVersion,
		"Legacy":        legacy,
		"Image":         imageHost + outdatedImagePath,
		"LocationsURL":  locationsURL,
	}
}

// imageHost is the host (with port) of the server's public URL, where the
// `current` image is served; a placeholder when no base URL is configured.
func imageHost(baseURL string) string {
	if parsed, err := url.Parse(strings.TrimSpace(baseURL)); err == nil && parsed.Host != "" {
		return parsed.Host
	}

	return "<server-host>"
}

// privateLocationsURL links to Organization → Private locations, empty without
// a base URL.
func privateLocationsURL(baseURL, orgSlug string) string {
	if strings.TrimSpace(baseURL) == "" {
		return ""
	}

	return fmt.Sprintf("%s%s/orgs/%s/organization/private-locations",
		strings.TrimRight(baseURL, "/"), config.DashboardBasePath, orgSlug)
}
