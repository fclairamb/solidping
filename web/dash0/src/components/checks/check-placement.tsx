import { useTranslation } from "react-i18next";
import { Pin, Shuffle } from "lucide-react";

import { useEvents, type Check, type Event, type RegionDefinition } from "@/api/hooks";
import { formatClockTime } from "@/lib/check-freshness";
import { regionDisplayLabel } from "@/lib/region-label";

// The check detail page's placement block (spec 2026-09-25-06):
//
//   Placement: Automatic, 2 regions: paris, gravelines
//   Placement history: lauterbourg → paris at 13:47, region offline
//
// Everything here is what the server sends: `placement` / `regionCount` /
// `regionPool` on the check and the check.placement_changed events. The
// per-region result ages and the fail-quorum sentence are deliberately not
// rendered here: the ages ticked every second and redrew the whole block,
// and the regions are already named in the summary line while a regional
// issue is spelled out by the amber banner (spec 2026-09-25-10).

/** The placement_changed event type, as the server writes it. */
export const PLACEMENT_CHANGED_EVENT = "check.placement_changed";

/** How many placement moves the history shows. */
const HISTORY_SIZE = 5;

export function CheckPlacementDetail({
  org,
  check,
  regions,
}: {
  org: string;
  check: Check;
  regions?: RegionDefinition[];
}) {
  const { t } = useTranslation("checks");
  const placed = check.regions ?? [];
  const isAuto = check.placement === "auto";

  if (placed.length === 0 && !isAuto) {
    return null;
  }

  const names = placed.map((slug) => regionDisplayLabel(regions, slug));

  return (
    <div data-testid="check-placement" data-placement={check.placement ?? "pinned"}>
      <div className="text-sm font-medium text-muted-foreground mb-1">
        {t("detail.placement.title")}
      </div>
      <div className="flex items-center gap-1.5 text-sm" data-testid="check-placement-summary">
        {isAuto ? (
          <Shuffle className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
        ) : (
          <Pin className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
        )}
        <span>
          {isAuto
            ? t("detail.placement.auto", {
                count: check.regionCount ?? placed.length,
                regions: names.join(", "),
              })
            : t("detail.placement.pinned", { regions: names.join(", ") })}
        </span>
      </div>
      {isAuto && (check.regionPool?.length ?? 0) > 0 && (
        <div className="text-xs text-muted-foreground" data-testid="check-placement-pool">
          {t("detail.placement.pool", {
            regions: (check.regionPool ?? []).map((slug) => regionDisplayLabel(regions, slug)).join(", "),
          })}
        </div>
      )}
      <PlacementHistory org={org} checkUid={check.uid} regions={regions} />
    </div>
  );
}

/** The last automatic moves of the check, newest first. Renders nothing when
 * the check never moved. */
function PlacementHistory({
  org,
  checkUid,
  regions,
}: {
  org: string;
  checkUid?: string;
  regions?: RegionDefinition[];
}) {
  const { t, i18n } = useTranslation("checks");
  const { data } = useEvents(org, {
    checkUid,
    eventType: PLACEMENT_CHANGED_EVENT,
    size: HISTORY_SIZE,
  });
  const moves = (data?.data ?? []).filter((event) => event.eventType === PLACEMENT_CHANGED_EVENT);

  if (!checkUid || moves.length === 0) {
    return null;
  }

  return (
    <div className="mt-2" data-testid="check-placement-history">
      <div className="text-xs font-medium text-muted-foreground">{t("detail.placement.history")}</div>
      <ul className="mt-0.5 space-y-0.5 text-xs">
        {moves.map((event) => (
          <li key={event.uid} data-testid="check-placement-history-entry">
            {describeMove(event, regions, t, i18n.language)}
          </li>
        ))}
      </ul>
    </div>
  );
}

function describeMove(
  event: Event,
  regions: RegionDefinition[] | undefined,
  t: (key: string, options?: Record<string, unknown>) => string,
  language: string,
): string {
  const payload = event.payload ?? {};
  const from = typeof payload.from === "string" ? payload.from : "";
  const to = typeof payload.to === "string" ? payload.to : "";
  const reason = typeof payload.reason === "string" ? payload.reason : "";

  return t("detail.placement.historyEntry", {
    from: regionDisplayLabel(regions, from),
    to: regionDisplayLabel(regions, to),
    time: event.createdAt ? formatClockTime(event.createdAt, language) : "",
    reason:
      reason === "region_offline"
        ? t("detail.placement.reasons.region_offline")
        : t("detail.placement.reasons.other"),
  });
}
