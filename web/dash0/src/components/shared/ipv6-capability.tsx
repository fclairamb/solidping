import { Badge } from "@/components/ui/badge";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";

/**
 * Three-state IPv6 egress capability advertised by a region's LIVE workers
 * (spec 2026-08-15-11).
 *
 * "unknown" is a REAL state — no live worker, or none reporting yet — and must
 * never be rendered as "no". Collapsing it to a negative is exactly the lie the
 * spec exists to stop telling: an older agent that does not report may be
 * perfectly v6-capable.
 *
 * The advertised value is a UI hint only. It never gates execution: the
 * run-time egress probe stays the authority, so nothing here may hide, filter
 * or disable a region.
 */
export type Ipv6Capability = "yes" | "no" | "unknown";

/** Reads the `ipv6` key out of a region's capability map. An absent map (the
 * field is omitempty, and older servers never send it at all) is "unknown". */
export function ipv6Capability(
  capabilities?: Record<string, string> | null
): Ipv6Capability {
  const value = capabilities?.ipv6;

  if (value === "yes" || value === "no") {
    return value;
  }

  return "unknown";
}

const labels: Record<Ipv6Capability, string> = {
  yes: "IPv6",
  no: "No IPv6",
  unknown: "IPv6 unknown",
};

const variants: Record<Ipv6Capability, "success" | "warning" | "outline"> = {
  yes: "success",
  no: "warning",
  // Neutral on purpose — "we have not been told" is not a negative.
  unknown: "outline",
};

const hints: Record<Ipv6Capability, string> = {
  yes: "At least one live worker in this region reports IPv6 egress.",
  no: "No live worker in this region currently reports IPv6 egress. You can still select it — the check is only rejected if the worker really has no IPv6 route when it runs.",
  unknown:
    "Not reported yet — this region has no live worker reporting its egress families, or is running an older agent. It may well support IPv6.",
};

/**
 * Renders the capability as a badge with a distinct look per state. Pass
 * `hideUnknown` where an inline surface (the region picker) would otherwise be
 * a wall of neutral badges — the absence of a badge then reads as "not
 * reported", never as "no IPv6", because "no" always renders.
 */
export function Ipv6CapabilityBadge({
  capability,
  hideUnknown = false,
  className,
  "data-testid": testId,
}: {
  capability: Ipv6Capability;
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
          <Badge
            variant={variants[capability]}
            className={className}
            data-testid={testId}
            data-ipv6={capability}
          >
            {labels[capability]}
          </Badge>
        </TooltipTrigger>
        <TooltipContent className="max-w-xs">{hints[capability]}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}
