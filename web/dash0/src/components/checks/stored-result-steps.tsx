import { useTranslation } from "react-i18next";

import { useCheckScreenshots, type CheckScreenshot } from "@/api/hooks";
import { StepsList } from "@/components/checks/steps-list";
import type { CheckStep } from "@/lib/check-steps";

/** Slack around the result's execution window (clock skew, storage lag). */
const CAPTURE_SLACK_MS = 5000;

/** The newest capture taken during the result's own execution window, so the
 * thumbnail never shows another run's screenshot. */
export function matchCapture(
  screenshots: CheckScreenshot[] | undefined,
  timestamp: string | undefined,
  durationMs: number | undefined,
): CheckScreenshot | undefined {
  if (!screenshots || !timestamp) return undefined;
  const start = Date.parse(timestamp);
  if (Number.isNaN(start)) return undefined;
  const from = start - CAPTURE_SLACK_MS;
  const to = start + (durationMs ?? 0) + CAPTURE_SLACK_MS;
  return screenshots.find((shot) => {
    const at = Date.parse(shot.capturedAt);
    return !Number.isNaN(at) && at >= from && at <= to;
  });
}

/** The steps of a stored result; the persisted capture (the one taken during
 * this result's run) is shown as a thumbnail under the step that owns it. */
export function StoredResultSteps({
  org,
  checkUid,
  steps,
  timestamp,
  durationMs,
}: {
  org: string;
  checkUid: string;
  steps: CheckStep[];
  timestamp?: string;
  durationMs?: number;
}) {
  const { t } = useTranslation("checks");
  const listing = useCheckScreenshots(org, checkUid);
  const hasShot = steps.some((step) => step.screenshot);
  const src = hasShot
    ? matchCapture(listing.data?.screenshots, timestamp, durationMs)?.downloadUrl
    : undefined;
  return (
    <div data-testid="check-result-steps">
      <div className="text-sm font-medium text-muted-foreground mb-2">
        {t("steps.title")}
      </div>
      <StepsList steps={steps} screenshotSrc={src} />
    </div>
  );
}
