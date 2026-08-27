import { useTranslation } from "react-i18next";
import { Link } from "@tanstack/react-router";
import { AlertTriangle, ExternalLink } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import type { EntitlementsChecksPerMinute } from "@/api/hooks";
import {
  formatCheckRateDemand,
  shouldWarnAboutCheckRate,
} from "@/lib/check-rate-limit";

interface CheckRateLimitBannerProps {
  org: string;
  /** `checksPerMinute` from the entitlements payload. */
  checksPerMinute?: EntitlementsChecksPerMinute | null;
  /** SaaS upgrade target, when the deployment configures one. */
  upgradeUrl?: string;
  /**
   * Render the link to the remedy — the check scheduling page, where the
   * demand can actually be brought back under the cap. Off on the scheduling
   * page itself, where the link would point at the page the reader is already
   * on.
   */
  showUsageLink?: boolean;
}

/**
 * CheckRateLimitBanner tells an organization that its check executions are
 * being skipped because it is over its `maxChecksPerMinute` entitlement
 * (spec 2026-08-26-03).
 *
 * Before this existed the only traces of a rate-limited org were an INFO log
 * line and a Prometheus counter — neither of which reaches the customer. What
 * the customer saw was unexplained gaps in their results, which reads as "the
 * product is broken" rather than "you are over your plan".
 *
 * Org-level on purpose: the deferral rotates across all of the org's checks
 * (spec 2026-08-26-02), so a per-check flag would light up almost everywhere
 * and carry no information. A warning, never destructive — nothing is lost
 * permanently and the remedy is the customer's to choose.
 *
 * Renders nothing when the org is inside its cap — including right after the
 * scheduling has been reviewed back under it, even if executions were skipped
 * earlier today. The warning tracks the live state; the skip count is only
 * supporting detail while the org is over.
 */
export function CheckRateLimitBanner({
  org,
  checksPerMinute,
  upgradeUrl,
  showUsageLink = false,
}: CheckRateLimitBannerProps) {
  const { t } = useTranslation(["org"]);

  if (!shouldWarnAboutCheckRate(checksPerMinute) || !checksPerMinute) {
    return null;
  }

  return (
    <Alert variant="warning" data-testid="check-rate-limit-banner">
      <AlertTriangle />
      <AlertTitle>{t("org:checkRateLimit.title")}</AlertTitle>
      <AlertDescription className="space-y-2">
        <p>
          {t("org:checkRateLimit.overLimit", {
            demand: formatCheckRateDemand(checksPerMinute.demand),
            limit: checksPerMinute.limit,
          })}
        </p>
        {checksPerMinute.skippedToday > 0 && (
          <p data-testid="check-rate-limit-skipped">
            {t("org:checkRateLimit.skippedToday", {
              count: checksPerMinute.skippedToday,
            })}
          </p>
        )}
        {(showUsageLink || upgradeUrl) && (
          <div className="flex flex-wrap gap-2 pt-1">
            {showUsageLink && (
              // The scheduling page (spec 2026-08-26-04), not the Usage page:
              // a banner about being over the cap should hand the reader the
              // surface that can fix it, not one that restates the number.
              <Button asChild variant="outline" size="sm">
                <Link
                  to="/orgs/$org/checks/scheduling"
                  params={{ org }}
                  data-testid="check-rate-limit-usage-link"
                >
                  {t("org:checkRateLimit.viewUsage")}
                </Link>
              </Button>
            )}
            {upgradeUrl && (
              <Button asChild size="sm">
                <a
                  href={upgradeUrl}
                  target="_blank"
                  rel="noreferrer"
                  data-testid="check-rate-limit-upgrade-link"
                >
                  {t("org:checkRateLimit.upgrade")}
                  <ExternalLink className="ml-2 h-3.5 w-3.5" />
                </a>
              </Button>
            )}
          </div>
        )}
      </AlertDescription>
    </Alert>
  );
}
