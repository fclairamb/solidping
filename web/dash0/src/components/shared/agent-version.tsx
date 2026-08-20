import { Badge } from "@/components/ui/badge";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";

/**
 * Compares an agent's self-reported build version against this server's own
 * (spec 2026-08-19-07).
 *
 * UNLIKE the IPv6 capability tri-state (ipv6-capability.tsx), this is not a
 * value stored on the wire — it is a comparison computed here, at read time,
 * from two two-state inputs: the agent's `version` (`null` = never
 * reported) and the server's own version (from `useVersion()`, possibly
 * still loading).
 *
 * "unknown" is a REAL state — no live agent has ever reported, or the
 * server's own version has not loaded yet — and must never be rendered as
 * "drifted". An old agent that predates this feature must not look broken.
 */
export type AgentVersionState = "match" | "drifted" | "unknown";

export function agentVersionState(
  agentVersion?: string | null,
  serverVersion?: string | null
): AgentVersionState {
  if (!agentVersion || !serverVersion) {
    return "unknown";
  }

  return agentVersion === serverVersion ? "match" : "drifted";
}

/**
 * Renders an agent's version against this server's own. Matching and
 * unknown render as plain text (no badge — a match is not noteworthy, and
 * "we don't know" is not a warning); only "drifted" gets an amber badge,
 * exactly the same visual weight `Ipv6CapabilityBadge` gives "no" — a real,
 * actionable signal, not a decoration.
 */
export function AgentVersionCell({
  agentVersion,
  serverVersion,
  className,
  "data-testid": testId,
}: {
  agentVersion?: string | null;
  serverVersion?: string | null;
  className?: string;
  "data-testid"?: string;
}) {
  const state = agentVersionState(agentVersion, serverVersion);

  if (state === "unknown") {
    return (
      <span
        className={`text-sm text-muted-foreground ${className ?? ""}`}
        data-testid={testId}
        data-agent-version="unknown"
      >
        unknown
      </span>
    );
  }

  if (state === "match") {
    return (
      <span className={`text-sm ${className ?? ""}`} data-testid={testId} data-agent-version="match">
        {agentVersion}
      </span>
    );
  }

  return (
    <TooltipProvider>
      <Tooltip>
        <TooltipTrigger asChild>
          <span
            className={`inline-flex items-center gap-1.5 ${className ?? ""}`}
            data-testid={testId}
            data-agent-version="drifted"
          >
            <span className="text-sm">{agentVersion}</span>
            <Badge variant="warning">Drifted</Badge>
          </span>
        </TooltipTrigger>
        <TooltipContent className="max-w-xs">This server runs v{serverVersion}.</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}
