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
  onClose: () => void;
}

const BOX_WIDTH = 240;
const MARGIN = 8;

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
  onClose,
}: PinnedResultBoxProps) {
  const { data, isLoading, error } = useResult(org, checkUid, resultUid);

  let leftPx = MARGIN;
  let topPx = MARGIN;
  if (anchor) {
    const half = BOX_WIDTH / 2;
    leftPx = Math.min(Math.max(anchor.cx - half, MARGIN), width - BOX_WIDTH - MARGIN);
    topPx = Math.max(anchor.cy - 8 - 140, MARGIN);
  }

  const ts = data?.periodStart ? new Date(data.periodStart) : null;

  return (
    <div
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
          aria-label="Close"
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
        <p className="text-xs text-destructive mt-2">Could not load details</p>
      )}

      {data && !isLoading && (
        <div className="space-y-1 mt-2">
          <div className="flex items-baseline justify-between gap-2">
            <span className="text-xs text-muted-foreground">Duration</span>
            <span className="font-medium">{formatMs(data.durationMs)}</span>
          </div>
          {data.durationMinMs != null && data.durationMaxMs != null && (
            <div className="flex items-baseline justify-between gap-2">
              <span className="text-xs text-muted-foreground">Min / Max</span>
              <span className="text-xs">
                {formatMs(data.durationMinMs)} / {formatMs(data.durationMaxMs)}
              </span>
            </div>
          )}
          {data.availabilityPct != null && (
            <div className="flex items-baseline justify-between gap-2">
              <span className="text-xs text-muted-foreground">Availability</span>
              <span className="text-xs">{formatPct(data.availabilityPct)}</span>
            </div>
          )}
          {data.region && (
            <div className="flex items-baseline justify-between gap-2">
              <span className="text-xs text-muted-foreground">Region</span>
              <span className="text-xs">{data.region}</span>
            </div>
          )}
          {data.status && (
            <div className="flex items-baseline justify-between gap-2">
              <span className="text-xs text-muted-foreground">Status</span>
              <Badge variant="outline" className="capitalize text-xs">
                {data.status}
              </Badge>
            </div>
          )}
          {data.periodType && data.periodType !== "raw" && (
            <div className="flex items-baseline justify-between gap-2">
              <span className="text-xs text-muted-foreground">Period</span>
              <Badge variant="outline" className="text-xs">
                {data.periodType}
              </Badge>
            </div>
          )}
        </div>
      )}

      <p className="mt-2 text-xs text-muted-foreground">
        Click again to open full page
      </p>
    </div>
  );
}
