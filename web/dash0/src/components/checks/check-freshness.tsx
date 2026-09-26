import { useTranslation } from "react-i18next";
import { Clock } from "lucide-react";

import type { Check, RegionDefinition } from "@/api/hooks";
import { LiveDurationAgo } from "@/components/shared/relative-time";
import { formatClockTime, lastRealResultAt, splitFreshness } from "@/lib/check-freshness";
import { regionDisplayLabel } from "@/lib/region-label";
import { cn } from "@/lib/utils";

// The "No data" (stale) surfaces of the check detail page, spec 2026-09-25-02.
// Both read only what the server sends: `status`, `lastResultAt` and the
// per-region `regionFreshness` list (with=region_freshness).

/**
 * "No data since 13:41" beside the status badge of a stale check — the time
 * of the newest real result, so the reader knows how long nobody has been
 * looking.
 */
export function StaleSince({ check }: { check: Check }) {
  const { t, i18n } = useTranslation("checks");
  if (check.status !== "stale") return null;

  const last = lastRealResultAt(check);
  return (
    <span
      data-testid="check-stale-since"
      className="inline-flex items-center gap-1 text-sm text-muted-foreground"
    >
      <Clock className="h-3.5 w-3.5" aria-hidden="true" />
      {last
        ? t("detail.freshness.noDataSince", { time: formatClockTime(last, i18n.language) })
        : t("detail.freshness.noDataEver")}
    </span>
  );
}

function regionName(regions: RegionDefinition[] | undefined, slug: string, fallback: string): string {
  return slug === "" ? fallback : regionDisplayLabel(regions, slug);
}

/**
 * Per-region freshness: "no result from lauterbourg since 13:41, 2 other
 * regions reporting", then every region with its own age. A check still
 * reporting from one region keeps its status — this list is how the silent
 * region shows up anyway. Rendered only when there is something to say (the
 * check is stale, or at least one region is silent).
 */
export function RegionFreshnessList({
  check,
  regions,
}: {
  check: Check;
  regions?: RegionDefinition[];
}) {
  const { t, i18n } = useTranslation("checks");
  const freshness = check.regionFreshness ?? [];
  const { silent, reporting } = splitFreshness(freshness);

  if (freshness.length === 0 || (silent.length === 0 && check.status !== "stale")) {
    return null;
  }

  const defaultRegion = t("detail.freshness.defaultRegion");

  return (
    <div data-testid="region-freshness">
      <div className="text-sm font-medium text-muted-foreground">
        {t("detail.freshness.title")}
      </div>
      {silent.length > 0 && (
        <ul className="mt-1 space-y-0.5 text-sm">
          {silent.map((region) => (
            <li key={`silent-${region.region}`} data-testid="region-freshness-silent">
              {region.lastResultAt
                ? t("detail.freshness.silentRegion", {
                    region: regionName(regions, region.region, defaultRegion),
                    time: formatClockTime(region.lastResultAt, i18n.language),
                    count: reporting.length,
                  })
                : t("detail.freshness.silentRegionNever", {
                    region: regionName(regions, region.region, defaultRegion),
                    count: reporting.length,
                  })}
            </li>
          ))}
        </ul>
      )}
      <ul className="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
        {freshness.map((region) => (
          <li
            key={region.region}
            data-testid="region-freshness-row"
            data-region={region.region}
            data-stale={region.stale ? "true" : "false"}
            className={cn("inline-flex items-center gap-1", region.stale && "font-medium")}
          >
            {region.stale && <Clock className="h-3 w-3" aria-hidden="true" />}
            <span>{regionName(regions, region.region, defaultRegion)}</span>
            <span>·</span>
            {region.lastResultAt ? (
              <LiveDurationAgo since={region.lastResultAt} />
            ) : (
              <span>{t("detail.freshness.noResultRecently")}</span>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}
