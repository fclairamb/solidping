package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// capNoticeChannelLabel names a usage-counter kind in the admin email.
func capNoticeChannelLabel(kind string) string {
	switch kind {
	case models.UsageCounterKindVoice:
		return "voice call"
	case models.UsageCounterKindWhatsApp:
		return "WhatsApp"
	default:
		return "SMS"
	}
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

	limit := 0

	var quotaErr *entitlements.QuotaError
	if errors.As(refusal, &quotaErr) {
		limit = quotaErr.Limit
	}

	usageURL := ""
	if base := appBaseURL(jctx); base != "" {
		usageURL = fmt.Sprintf("%s%s/orgs/%s/organization/usage", base, config.DashboardBasePath, org.Slug)
	}

	data := map[string]any{
		"Channel":    capNoticeChannelLabel(kind),
		"Limit":      limit,
		"UsageURL":   usageURL,
		"UpgradeURL": jctx.Services.Entitlements.UpgradeURL(ctx, org.Slug),
	}
	email.ApplyOrgBranding(data, org.Name, org.Slug, org.LogoURL)

	for _, member := range members {
		if !member.Role.AtLeast(models.MemberRoleAdmin) || member.User == nil || member.User.Email == "" {
			continue
		}

		// One job per recipient: a shared To: would disclose the admin list.
		raw, marshalErr := json.Marshal(EmailJobConfig{
			To:           []string{member.User.Email},
			Template:     email.TemplateUsageCapReached,
			TemplateData: data,
		})
		if marshalErr != nil {
			log.ErrorContext(ctx, "monthly cap notice: cannot marshal the email", "error", marshalErr)

			return
		}

		if _, jobErr := jctx.Services.Jobs.CreateJob(ctx, orgUID, string(jobdef.JobTypeEmail), raw, nil); jobErr != nil {
			log.ErrorContext(ctx, "monthly cap notice: cannot enqueue the email", "orgUID", orgUID, "error", jobErr)
		}
	}
}
