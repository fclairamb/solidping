import { useTranslation } from "react-i18next";

import { useCheckScreenshots } from "@/api/hooks";
import { StepsList } from "@/components/checks/steps-list";
import type { CheckStep } from "@/lib/check-steps";

/** The steps of a stored result; the persisted capture (the check's latest
 * stored screenshot) is shown as a thumbnail under the step that owns it. */
export function StoredResultSteps({
  org,
  checkUid,
  steps,
}: {
  org: string;
  checkUid: string;
  steps: CheckStep[];
}) {
  const { t } = useTranslation("checks");
  const listing = useCheckScreenshots(org, checkUid);
  const hasShot = steps.some((step) => step.screenshot);
  const src = hasShot ? listing.data?.screenshots?.[0]?.downloadUrl : undefined;
  return (
    <div data-testid="check-result-steps">
      <div className="text-sm font-medium text-muted-foreground mb-2">
        {t("steps.title")}
      </div>
      <StepsList steps={steps} screenshotSrc={src} />
    </div>
  );
}
