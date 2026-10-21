package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/i18n/recipient"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// Channels of the usage-cap-reached.html view model. Each picks its own set of
// sentences (alerts.usageCap.<channel>.*): the channel noun is never spliced
// into a sentence, since languages word "SMS alerts" and "voice call alerts"
// differently.
const (
	capNoticeChannelSMS      = "sms"
	capNoticeChannelVoice    = "voice"
	capNoticeChannelWhatsApp = "whatsapp"
)

// capNoticeChannel maps a usage-counter kind to its usage-cap-reached.html
// channel.
func capNoticeChannel(kind string) string {
	switch kind {
	case models.UsageCounterKindVoice:
		return capNoticeChannelVoice
	case models.UsageCounterKindWhatsApp:
		return capNoticeChannelWhatsApp
	default:
		return capNoticeChannelSMS
	}
}

// UsageCapEmailData is the usage-cap-reached.html view model, without the org
// branding the caller adds. kind is a models.UsageCounterKind*; resetsAt is
// when the allowance comes back (the next UTC month), shown in the reader's
// zone. The email preview builds its fixture through it too.
func UsageCapEmailData(kind string, limit int, resetsAt time.Time, usageURL, upgradeURL string) map[string]any {
	return map[string]any{
		"Channel":    capNoticeChannel(kind),
		"Limit":      limit,
		"ResetsAt":   resetsAt,
		"UsageURL":   usageURL,
		"UpgradeURL": upgradeURL,
	}
}

// nextUTCMonth is the start of the UTC month after now: monthly allowances
// are counted per UTC month.
func nextUTCMonth(now time.Time) time.Time {
	now = now.UTC()

	return time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
}

// notifyMonthlyCapReached tells the org's owners and admins that a paging
// channel ran out of its monthly allowance, so a dropped page is not silent.
// It sends at most once per org, per kind, per UTC month (claimed atomically
// in org_usage_counters). A runaway-guard refusal never reaches here.
//
// Every failure is logged and swallowed: the escalation step must go on to
// the other channels whatever happens to this mail.
func notifyMonthlyCapReached(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, orgUID, kind string, refusal error,
) {
	if jctx.Services == nil || jctx.Services.Entitlements == nil || jctx.Services.Jobs == nil {
		return
	}

	claimed, err := jctx.Services.Entitlements.ClaimMonthlyCapNotice(ctx, orgUID, kind)
	if err != nil {
		log.WarnContext(ctx, "cannot claim the monthly cap notice", "orgUID", orgUID, "kind", kind, "error", err)

		return
	}

	if !claimed {
		return
	}

	org, err := jctx.DBService.GetOrganization(ctx, orgUID)
	if err != nil || org == nil {
		log.WarnContext(ctx, "monthly cap notice: cannot load the organization", "orgUID", orgUID, "error", err)

		return
	}

	members, err := jctx.DBService.ListMembersByOrg(ctx, orgUID)
	if err != nil {
		log.WarnContext(ctx, "monthly cap notice: cannot list the organization's admins",
			"orgUID", orgUID, "error", err)

		return
	}

	data := capNoticeData(ctx, jctx, org, kind, refusal)

	for _, member := range members {
		if !member.Role.AtLeast(models.MemberRoleAdmin) || member.User == nil || member.User.Email == "" {
			continue
		}

		enqueueCapNotice(ctx, jctx, log, orgUID, member.User, data)
	}
}

// capNoticeData builds the email view model.
func capNoticeData(
	ctx context.Context, jctx *jobdef.JobContext, org *models.Organization, kind string, refusal error,
) map[string]any {
	limit := 0

	var quotaErr *entitlements.QuotaError
	if errors.As(refusal, &quotaErr) {
		limit = quotaErr.Limit
	}

	usageURL := ""
	if base := appBaseURL(jctx); base != "" {
		usageURL = fmt.Sprintf("%s%s/orgs/%s/organization/usage", base, config.DashboardBasePath, org.Slug)
	}

	data := UsageCapEmailData(kind, limit, nextUTCMonth(jctx.ClockNow()), usageURL,
		jctx.Services.Entitlements.UpgradeURL(ctx, org.Slug))
	email.ApplyOrgBranding(data, org.Name, org.Slug, org.LogoURL)

	return data
}

// enqueueCapNotice queues one email job, in the admin's language and zone.
// One job per recipient: a shared To: would disclose the admin list.
func enqueueCapNotice(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, orgUID string, admin *models.User,
	data map[string]any,
) {
	loc := recipient.ForUser(admin)

	raw, err := json.Marshal(EmailJobConfig{
		To:           []string{admin.Email},
		Template:     email.TemplateUsageCapReached,
		TemplateData: data,
		Locale:       loc.Lang(),
		Timezone:     loc.Timezone(),
	})
	if err != nil {
		log.ErrorContext(ctx, "monthly cap notice: cannot marshal the email", "error", err)

		return
	}

	if _, err := jctx.Services.Jobs.CreateJob(ctx, orgUID, string(jobdef.JobTypeEmail), raw, nil); err != nil {
		log.ErrorContext(ctx, "monthly cap notice: cannot enqueue the email", "orgUID", orgUID, "error", err)
	}
}
