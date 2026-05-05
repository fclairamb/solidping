import { useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { format } from "date-fns";
import { X } from "lucide-react";
import { useResult } from "@/api/hooks";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

interface PinnedResultBoxProps {
  org: string;
  checkUid: string;
  resultUid: string;
  anchor?: { cx: number; cy: number };
  width: number;
  height: number;
  onClose: () => void;
}

const BOX_WIDTH = 240;
const MARGIN = 8;
// Vertical distance between the anchor dot and the box edge, so the box
// never sits directly on top of the data point.
const DOT_GAP = 12;

function formatMs(ms?: number) {
  if (ms == null) return "—";
  if (ms < 1000) return `${ms.toFixed(0)} ms`;
  return `${(ms / 1000).toFixed(2)} s`;
}

function formatPct(pct?: number) {
  if (pct == null) return "—";
  return `${pct.toFixed(1)}%`;
}

export function PinnedResultBox({
  org,
  checkUid,
  resultUid,
  anchor,
  width,
  height,
  onClose,
}: PinnedResultBoxProps) {
  const { t } = useTranslation("checks");
  const { data, isLoading, error } = useResult(org, checkUid, resultUid);

  const boxRef = useRef<HTMLDivElement | null>(null);
  const [boxHeight, setBoxHeight] = useState(140);
  useLayoutEffect(() => {
    if (boxRef.current) setBoxHeight(boxRef.current.offsetHeight);
  }, [data, isLoading, error]);

  let leftPx = MARGIN;
  let topPx = MARGIN;
  if (anchor) {
    // Horizontal: center on the dot, but never let the upper bound fall
    // below MARGIN (which would happen when `width` hasn't been measured
    // yet and the naive clamp would push the box off-screen).
    const half = BOX_WIDTH / 2;
    const upperLeft = Math.max(width - BOX_WIDTH - MARGIN, MARGIN);
    leftPx = Math.max(MARGIN, Math.min(anchor.cx - half, upperLeft));

    // Vertical: prefer above the dot; flip below if no room. Falls back to
    // the top margin if neither side fits (very small chart).
    const above = anchor.cy - DOT_GAP - boxHeight;
    const below = anchor.cy + DOT_GAP;
    if (above >= MARGIN) {
      topPx = above;
    } else if (height === 0 || below + boxHeight <= height - MARGIN) {
      topPx = below;
    } else {
      topPx = MARGIN;
    }
  }

  const ts = data?.periodStart ? new Date(data.periodStart) : null;

  return (
    <div
      ref={boxRef}
      className="absolute z-10 rounded-md border bg-popover p-3 text-sm shadow-md"
      style={{ left: leftPx, top: topPx, width: BOX_WIDTH }}
      onClick={(e) => e.stopPropagation()}
    >
      <div className="flex items-start justify-between gap-2">
        <p className="text-xs text-muted-foreground">
          {ts ? format(ts, "MMM d, HH:mm:ss") : ""}
        </p>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="h-5 w-5 p-0"
          onClick={onClose}
          aria-label={t("detail.resultBox.close")}
        >
          <X className="h-3 w-3" />
        </Button>
      </div>

      {isLoading && (
        <div className="space-y-1 mt-2">
          <Skeleton className="h-4 w-24" />
          <Skeleton className="h-4 w-32" />
          <Skeleton className="h-4 w-20" />
        </div>
      )}

      {error && (
        <p className="text-xs text-destructive mt-2">{t("detail.resultBox.couldNotLoad")}</p>
      )}

      {data && !isLoading && (
        <div className="space-y-1 mt-2">
          <div className="flex items-baseline justify-between gap-2">
            <span className="text-xs text-muted-foreground">{t("detail.resultBox.duration")}</span>
            <span className="font-medium">{formatMs(data.durationMs)}</span>
          </div>
          {data.durationMinMs != null && data.durationMaxMs != null && (
            <div className="flex items-baseline justify-between gap-2">
              <span className="text-xs text-muted-foreground">{t("detail.resultBox.minMax")}</span>
              <span className="text-xs">
                {formatMs(data.durationMinMs)} / {formatMs(data.durationMaxMs)}
              </span>
            </div>
          )}
          {data.availabilityPct != null && (
            <div className="flex items-baseline justify-between gap-2">
              <span className="text-xs text-muted-foreground">{t("detail.resultBox.availability")}</span>
              <span className="text-xs">{formatPct(data.availabilityPct)}</span>
            </div>
          )}
          {data.region && (
            <div className="flex items-baseline justify-between gap-2">
              <span className="text-xs text-muted-foreground">{t("detail.resultBox.region")}</span>
              <span className="text-xs">{data.region}</span>
            </div>
          )}
          {data.status && (
            <div className="flex items-baseline justify-between gap-2">
              <span className="text-xs text-muted-foreground">{t("detail.resultBox.status")}</span>
              <Badge variant="outline" className="capitalize text-xs">
                {data.status}
              </Badge>
            </div>
          )}
          {data.periodType && data.periodType !== "raw" && (
            <div className="flex items-baseline justify-between gap-2">
              <span className="text-xs text-muted-foreground">{t("detail.resultBox.period")}</span>
              <Badge variant="outline" className="text-xs">
                {data.periodType}
              </Badge>
            </div>
          )}
        </div>
      )}

      <p className="mt-2 text-xs text-muted-foreground">
        {t("detail.resultBox.clickAgain")}
      </p>
    </div>
  );
}
