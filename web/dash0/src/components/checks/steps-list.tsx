import { useTranslation } from "react-i18next";

import type { CheckStep } from "@/lib/check-steps";

const ICONS: Record<CheckStep["status"], string> = {
  ok: "✅",
  failed: "❌",
  skipped: "⏭",
};

export interface StepsListProps {
  steps: CheckStep[];
  /** Capture kept for the run (data URI or stored file URL), shown under the step that owns it. */
  screenshotSrc?: string;
}

/** The named steps of a script run: status, duration, error, thumbnail. */
export function StepsList({ steps, screenshotSrc }: StepsListProps) {
  const { t } = useTranslation("checks");
  if (steps.length === 0) return null;
  // Only one capture is kept per result (the last one): it belongs to the
  // last step that reports a screenshot.
  let ownerIndex = -1;
  steps.forEach((step, index) => {
    if (step.screenshot) ownerIndex = index;
  });
  return (
    <ol className="space-y-1 text-sm" data-testid="check-steps-list">
      {steps.map((step, index) => (
        <li
          key={`${index}-${step.name}`}
          className="space-y-1"
          data-testid="check-step-item"
          data-status={step.status}
        >
          <div className="flex items-center gap-2">
            <span aria-label={t(`steps.status.${step.status}`)}>{ICONS[step.status]}</span>
            <span className="font-medium">{step.name}</span>
            {step.status !== "skipped" && (
              <span className="text-xs text-muted-foreground" data-testid="check-step-duration">
                {step.durationMs} ms
              </span>
            )}
          </div>
          {step.error && (
            <p className="ml-7 text-xs text-destructive font-mono" data-testid="check-step-error">
              {step.error}
            </p>
          )}
          {index === ownerIndex && screenshotSrc && (
            <a href={screenshotSrc} target="_blank" rel="noreferrer" className="ml-7 block" data-testid="check-step-screenshot-link">
              <img
                src={screenshotSrc}
                alt={t("steps.screenshotAlt", { name: step.name })}
                className="max-h-32 rounded border"
                data-testid="check-step-screenshot"
              />
            </a>
          )}
        </li>
      ))}
    </ol>
  );
}
