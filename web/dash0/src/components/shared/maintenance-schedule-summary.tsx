import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { CalendarClock } from "lucide-react";

import type { MaintenanceOccurrence, MaintenanceWindow } from "@/api/hooks";
import {
  describeSchedule,
  formatOccurrenceDate,
  nextOccurrences,
} from "@/lib/maintenance-window-schedule";

// Accent-tinted panel that summarizes a maintenance window's schedule in plain
// language and previews its next few activations. Reused by the form (live,
// reflecting unsaved edits) and registered in the design reference.
//
// When `occurrences` is provided (e.g. the server-computed nextOccurrences) it
// is used as-is; otherwise the panel computes a client-side preview from the
// window via the UTC-anchored port.
export function MaintenanceScheduleSummary({
  window,
  occurrences,
  now,
  className,
}: {
  window: MaintenanceWindow;
  occurrences?: MaintenanceOccurrence[];
  now?: Date;
  className?: string;
}) {
  const { t } = useTranslation("maintenanceWindows");

  const summary = describeSchedule(window, t);

  const upcoming = useMemo(() => {
    if (occurrences) return occurrences;
    return nextOccurrences(window, now ?? new Date(), 3);
  }, [occurrences, window, now]);

  const nextLine =
    upcoming.length > 0
      ? `${t("summary.nextLabel")} ${upcoming
          .map((o) => formatOccurrenceDate(o.startAt))
          .join(" · ")}`
      : t("summary.nextNone");

  return (
    <div
      data-testid="mw-schedule-summary"
      className={[
        "rounded-lg border border-primary/20 bg-primary/5 p-4",
        className ?? "",
      ]
        .filter(Boolean)
        .join(" ")}
    >
      <div className="flex items-start gap-3">
        <CalendarClock className="mt-0.5 h-5 w-5 shrink-0 text-primary" />
        <div className="min-w-0 space-y-1">
          <p className="font-medium leading-snug">{summary || "—"}</p>
          <p className="text-sm text-muted-foreground">
            {t("summary.alertsPaused")}
          </p>
          <p className="text-sm text-muted-foreground break-words">
            {nextLine}
          </p>
        </div>
      </div>
    </div>
  );
}
