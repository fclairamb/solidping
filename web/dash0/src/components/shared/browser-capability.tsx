import { AppWindow } from "lucide-react";
import { cn } from "@/lib/utils";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";

/**
 * Three-state headless-browser capability advertised by a region's LIVE
 * workers (spec 2026-08-19-03), aggregated server-side with the same
 * `yes` / `no` / `unknown` semantics as IPv6
 * (`server/internal/regions/capabilities.go` — `CapabilityBrowser`).
 *
 * "unknown" is a REAL state — no live worker, or none reporting yet — and
 * must never be rendered as "no". An older agent that predates this feature,
 * or a region with no live worker at all, may well be able to run a browser
 * check; collapsing "unknown" into "no" would tell the user otherwise.
 *
 * The advertised value is a UI hint only. It never gates execution: the
 * run-time worker stays the authority, so nothing here may hide, filter or
 * disable a region.
 */
export type BrowserCapability = "yes" | "no" | "unknown";

/** Reads the `browser` key out of a region's capability map. An absent map
 * (the field is omitempty, and older servers never send it at all) is
 * "unknown". */
export function browserCapability(
  capabilities?: Record<string, string> | null
): BrowserCapability {
  const value = capabilities?.browser;

  if (value === "yes" || value === "no") {
    return value;
  }

  return "unknown";
}

const colors: Record<BrowserCapability, string> = {
  yes: "text-status-ok-foreground",
  no: "text-status-warning-foreground",
  // Neutral on purpose — "we have not been told" is not a negative.
  unknown: "text-muted-foreground",
};

const hints: Record<BrowserCapability, string> = {
  yes: "At least one live worker in this region reports a headless browser available for browser checks.",
  no: "No live worker in this region currently reports a headless browser. You can still select it — the check is only rejected if the worker really has no browser when it runs.",
  unknown:
    "Not reported yet — this region has no live worker reporting its capabilities, or is running an older agent. It may well support browser checks.",
};

/**
 * Renders the capability as a single icon whose color encodes the state —
 * deliberately icon-only, no label text, so it stays quiet next to the IPv6
 * text badge instead of crowding the region picker. Pass `hideUnknown` where
 * an inline surface would otherwise be a wall of neutral icons — the absence
 * of an icon then reads as "not reported", never as "no browser", because
 * "no" always renders.
 */
export function BrowserCapabilityIcon({
  capability,
  hideUnknown = false,
  className,
  "data-testid": testId,
}: {
  capability: BrowserCapability;
  hideUnknown?: boolean;
  className?: string;
  "data-testid"?: string;
}) {
  if (capability === "unknown" && hideUnknown) {
    return null;
  }

  return (
    <TooltipProvider>
      <Tooltip>
        <TooltipTrigger asChild>
          <AppWindow
            className={cn("h-3.5 w-3.5", colors[capability], className)}
            data-testid={testId}
            data-browser={capability}
            aria-label={hints[capability]}
          />
        </TooltipTrigger>
        <TooltipContent className="max-w-xs">{hints[capability]}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}
