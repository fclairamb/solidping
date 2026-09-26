import { useTranslation } from "react-i18next";
import { MapPinned } from "lucide-react";

import type { Check, RegionDefinition } from "@/api/hooks";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { regionDisplayLabel } from "@/lib/region-label";

/**
 * CheckRegionalIssueBanner tells the reader of one check that some, but fewer
 * than the quorum, of its regions are failing (spec 2026-09-25-10): the check
 * reads `warning`, and no incident opens until enough regions agree. The
 * status badge alone would say "warning", which is also what a checker warning
 * looks like; this names the failing regions and the rule.
 *
 * Everything comes from the server's `regionalIssue` block (detail,
 * with=region_freshness), evaluated exactly as the incident engine does.
 * Renders nothing without one, so a page can mount it unconditionally.
 */
export function CheckRegionalIssueBanner({
  check,
  regions,
}: {
  check: Pick<Check, "regionalIssue">;
  regions?: RegionDefinition[];
}) {
  const { t } = useTranslation("checks");
  const issue = check.regionalIssue;

  if (!issue || issue.failingRegions.length === 0) {
    return null;
  }

  return (
    <Alert variant="warning" data-testid="regional-issue-banner">
      <MapPinned />
      <AlertTitle>
        {t("detail.regionalIssue.title", {
          regions: issue.failingRegions.map((slug) => regionDisplayLabel(regions, slug)).join(", "),
        })}
      </AlertTitle>
      <AlertDescription>
        {t("detail.regionalIssue.detail", {
          failing: issue.failingRegions.length,
          count: issue.regionCount,
          quorum: issue.failQuorum,
        })}
      </AlertDescription>
    </Alert>
  );
}
