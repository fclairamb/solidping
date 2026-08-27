import { useTranslation } from "react-i18next";
import { ArrowRight, Infinity as InfinityIcon } from "lucide-react";
import { Progress } from "@/components/ui/progress";
import { cn } from "@/lib/utils";
import { formatRate } from "@/lib/check-scheduling";

export interface CheckRateMeterProps {
  /** Demand as currently saved on the server. */
  saved: number;
  /**
   * Demand the user's unsaved draft would produce. Pass the same value as
   * `saved` when nothing is pending; the "now → after" delta then hides itself.
   */
  draft: number;
  /** Resolved `maxChecksPerMinute`. null/undefined = unlimited. */
  limit?: number | null;
  className?: string;
}

/**
 * CheckRateMeter shows an org's scheduled check-execution rate against its
 * per-minute cap, and — while a draft is pending — where that rate would land
 * once applied.
 *
 * The "now → after" pair is the whole point: on the scheduling page a user
 * stretches a period to see whether it is enough, and the answer has to arrive
 * before anything is written. A meter that only ever showed the saved figure
 * would make the page a guessing game.
 *
 * Over the cap it goes amber, not red — matching `CheckRateLimitBanner`.
 * Being over is a plan mismatch that costs executions, not a destructive
 * failure, and the two surfaces sit on the same page and must agree.
 *
 * An absent limit is unlimited: no bar, no threshold, just the figure — the
 * same convention every other entitlement cap uses.
 */
export function CheckRateMeter({
  saved,
  draft,
  limit,
  className,
}: CheckRateMeterProps) {
  const { t } = useTranslation(["checks"]);

  const unlimited = limit === null || limit === undefined;
  const pending = Math.abs(draft - saved) > 1e-9;
  const over = !unlimited && draft > limit;

  return (
    <div
      className={cn("space-y-2", className)}
      data-testid="check-rate-meter"
      data-over={over ? "true" : "false"}
    >
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <span className="text-sm font-medium">
          {t("checks:scheduling.meter.label")}
        </span>
        <span className="flex items-baseline gap-1.5 text-sm tabular-nums">
          {pending && (
            <>
              <span
                className="text-muted-foreground line-through"
                data-testid="check-rate-meter-saved"
              >
                {formatRate(saved)}
              </span>
              <ArrowRight
                className="h-3 w-3 self-center text-muted-foreground"
                aria-hidden
              />
            </>
          )}
          <span
            className={cn(
              "font-semibold",
              over ? "text-status-warning-foreground" : "text-foreground",
            )}
            data-testid="check-rate-meter-total"
          >
            {formatRate(draft)}
          </span>
          <span className="text-muted-foreground">
            {unlimited ? (
              <span className="inline-flex items-center gap-1">
                /
                <InfinityIcon className="h-3.5 w-3.5" aria-hidden />
                <span className="sr-only">
                  {t("checks:scheduling.meter.unlimited")}
                </span>
              </span>
            ) : (
              `/ ${formatRate(limit)}`
            )}
          </span>
        </span>
      </div>
      {!unlimited && (
        <Progress
          value={draft}
          max={limit || 1}
          destructiveWhenFull={false}
          indicatorClassName={over ? "bg-status-warning" : undefined}
        />
      )}
      <p className="text-xs text-muted-foreground">
        {unlimited
          ? t("checks:scheduling.meter.unlimitedHint")
          : over
            ? t("checks:scheduling.meter.overHint")
            : t("checks:scheduling.meter.hint")}
      </p>
    </div>
  );
}
