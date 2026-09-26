import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "@tanstack/react-router";
import { WifiOff } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import type { RegionDefinition } from "@/api/hooks";
import { formatClockTime } from "@/lib/check-freshness";
import {
  checkRegionOutage,
  offlineRegionNames,
  summarizeRegionOutage,
} from "@/lib/region-outage";

/** How many check names the list banner spells out before "and N more". */
const LISTED_NAMES = 5;

interface CheckRegionOutageBannerProps {
  check: { regions?: string[]; enabled?: boolean };
  /** `regions` from useRegions(org). */
  regions?: RegionDefinition[];
}

/**
 * CheckRegionOutageBanner tells the reader of one check that a region it runs
 * from is offline (spec 2026-09-25-03).
 *
 * Blind (every region of the check is offline) is destructive: the check is
 * not running at all, and its "No data" status is our outage, not the
 * target's. Reduced (some regions still run) is a warning: the check keeps
 * running, from fewer places.
 *
 * Renders nothing when none of the check's regions is offline, so a page can
 * mount it unconditionally.
 */
export function CheckRegionOutageBanner({ check, regions }: CheckRegionOutageBannerProps) {
  const { t, i18n } = useTranslation(["checks"]);
  const outage = checkRegionOutage(check, regions);
  if (!outage) return null;

  const params = {
    regions: offlineRegionNames(outage.offline),
    time: outage.since ? formatClockTime(outage.since, i18n.language) : "?",
    live: outage.liveCount,
    total: outage.totalCount,
  };

  return (
    <Alert
      variant={outage.kind === "blind" ? "destructive" : "warning"}
      data-testid="region-outage-banner"
      data-kind={outage.kind}
    >
      <WifiOff />
      <AlertTitle>
        {outage.kind === "blind"
          ? t("checks:regionOutage.blindTitle", params)
          : t("checks:regionOutage.reducedTitle", params)}
      </AlertTitle>
      <AlertDescription>{t("checks:regionOutage.detail")}</AlertDescription>
    </Alert>
  );
}

interface ChecksRegionOutageBannerProps {
  org: string;
  checks: { uid: string; name?: string; slug?: string; regions?: string[]; enabled?: boolean }[];
  regions?: RegionDefinition[];
}

/**
 * ChecksRegionOutageBanner is the checks-list form of the same notice: one
 * alert naming the offline region(s), the listed checks that are not running
 * (linked), and how many keep running from their other regions. Destructive
 * when at least one check is blind, a warning otherwise.
 */
export function ChecksRegionOutageBanner({ org, checks, regions }: ChecksRegionOutageBannerProps) {
  const { t, i18n } = useTranslation(["checks"]);
  const summary = summarizeRegionOutage(checks, regions);
  if (!summary) return null;

  const shown = summary.blind.slice(0, LISTED_NAMES);
  const more = summary.blind.length - shown.length;

  const names = (
    <>
      {shown.map((check, index) => (
        <span key={check.uid}>
          {index > 0 && ", "}
          <Link
            to="/orgs/$org/checks/$checkUid"
            params={{ org, checkUid: check.uid }}
            className="underline underline-offset-2"
          >
            {check.name}
          </Link>
        </span>
      ))}
    </>
  );

  return (
    <Alert
      variant={summary.blind.length > 0 ? "destructive" : "warning"}
      data-testid="checks-region-outage-banner"
    >
      <WifiOff />
      <AlertTitle>
        {t("checks:regionOutage.listTitle", {
          regions: offlineRegionNames(summary.offline),
          time: summary.since ? formatClockTime(summary.since, i18n.language) : "?",
        })}
      </AlertTitle>
      <AlertDescription className="space-y-1">
        {summary.blind.length > 0 && (
          <p data-testid="checks-region-outage-blind">
            {splitAround(
              t("checks:regionOutage.listBlind", {
                count: summary.blind.length,
                names: more > 0 ? t("checks:regionOutage.andMore", { names: NAMES_TOKEN, count: more }) : NAMES_TOKEN,
              }),
              names,
            )}
          </p>
        )}
        {summary.reduced.length > 0 && (
          <p data-testid="checks-region-outage-reduced">
            {t("checks:regionOutage.listReduced", { count: summary.reduced.length })}
          </p>
        )}
      </AlertDescription>
    </Alert>
  );
}

/** Placeholder the translated sentence carries where the linked names go. */
const NAMES_TOKEN = "\u0000names\u0000";

/** Replaces NAMES_TOKEN in a translated sentence with rendered nodes. */
function splitAround(text: string, node: ReactNode) {
  const [before, after] = text.split(NAMES_TOKEN);
  if (after === undefined) return text;
  return (
    <>
      {before}
      {node}
      {after}
    </>
  );
}
