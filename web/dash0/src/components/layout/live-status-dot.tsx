import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useLiveConnectionStatus, type LiveConnectionStatus } from "@/contexts/LiveEventsContext";

/** Maps connection status to the dot color and its i18n label key. Follows
 * the existing ok/warning/error conventions (bg-green-500/bg-red-500, see
 * lib/status-style.ts) — not --destructive, which is reserved for
 * destructive actions. "connecting" and "disabled" share the same neutral
 * gray: both are "no live updates, and that's not an error". */
const STATUS_STYLE: Record<LiveConnectionStatus, { color: string; labelKey: string }> = {
  live: { color: "bg-green-500", labelKey: "liveStatus.live" },
  reconnecting: { color: "bg-red-500", labelKey: "liveStatus.reconnecting" },
  connecting: { color: "bg-gray-300", labelKey: "liveStatus.connecting" },
  disabled: { color: "bg-gray-300", labelKey: "liveStatus.unavailable" },
};

/**
 * Passive sidebar indicator for the org live-updates WebSocket: green while
 * streaming, red while dropped/retrying, gray while connecting or when
 * realtime is disabled/forbidden server-side. Purely informational — no
 * click action, no popover, no manual reconnect (see the spec's "out of
 * scope"). Mounted in the sidebar footer utility row alongside
 * LanguageSwitcher/ThemeToggle; nothing is added to the main page header.
 */
export function LiveStatusDot() {
  const { t } = useTranslation();
  const status = useLiveConnectionStatus();
  const { color, labelKey } = STATUS_STYLE[status];
  const label = t(labelKey);

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          data-testid="live-status-dot"
          data-status={status}
          aria-label={label}
          className={cn("inline-block h-2.5 w-2.5 shrink-0 rounded-full", color)}
        />
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}
