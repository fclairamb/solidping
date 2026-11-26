import { useTranslation } from "react-i18next";
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
 * Whether an agent needs a hand-made upgrade notice (spec 2026-11-26-03): live
 * (active), reporting a version that differs from the server's, and not
 * pinned on purpose. Unknown is never outdated.
 */
export function isAgentOutdated(
  agent: { status: string; version?: string | null; versionPinned?: boolean },
  serverVersion?: string | null
): boolean {
  return (
    agent.status === "active" &&
    !agent.versionPinned &&
    agentVersionState(agent.version, serverVersion) === "drifted"
  );
}

/**
 * Whether an active agent runs a version below the server's supported window
 * (spec 2026-11-26-04): the API says so through `versionSupported: false`.
 */
export function isAgentUnsupported(agent: { status: string; versionSupported?: boolean }): boolean {
  return agent.status === "active" && agent.versionSupported === false;
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
  pinned,
  supported = true,
  className,
  "data-testid": testId,
}: {
  agentVersion?: string | null;
  serverVersion?: string | null;
  /** The agent runs with SP_AGENT_IGNORE_VERSION_DRIFT=true (spec
   * 2026-11-22-04): its drift is deliberate, labelled "Pinned". */
  pinned?: boolean;
  /** `false` when the server says the version is below its minimum supported
   * agent version (spec 2026-11-26-04): red "Unsupported" instead of "Drifted".
   * Ignored when the version is unknown. */
  supported?: boolean;
  className?: string;
  "data-testid"?: string;
}) {
  const { t } = useTranslation("common");
  const state = agentVersionState(agentVersion, serverVersion);

  if (state === "unknown") {
    return (
      <span
        className={`text-sm text-muted-foreground ${className ?? ""}`}
        data-testid={testId}
        data-agent-version="unknown"
      >
        {t("agentVersion.unknown")}
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

  const unsupported = !supported;

  return (
    <TooltipProvider>
      <Tooltip>
        <TooltipTrigger asChild>
          <span
            className={`inline-flex items-center gap-1.5 ${className ?? ""}`}
            data-testid={testId}
            data-agent-version={unsupported ? "unsupported" : "drifted"}
          >
            <span className="text-sm">{agentVersion}</span>
            {unsupported ? (
              <Badge
                variant="destructive"
                data-testid={testId ? `${testId}-unsupported` : undefined}
              >
                {t("agentVersion.unsupported")}
              </Badge>
            ) : (
              <Badge variant="warning">{t("agentVersion.drifted")}</Badge>
            )}
            {pinned && (
              <Badge
                variant="secondary"
                data-testid={testId ? `${testId}-pinned` : undefined}
                data-agent-version-pinned="true"
              >
                {t("agentVersion.pinned")}
              </Badge>
            )}
          </span>
        </TooltipTrigger>
        <TooltipContent className="max-w-xs">
          {unsupported && <>{t("agentVersion.unsupportedHint")} </>}
          {t("agentVersion.serverRuns", { version: serverVersion })}
          {pinned && <> {t("agentVersion.pinnedHint")}</>}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}
