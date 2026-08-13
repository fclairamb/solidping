import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import {
  AlertTriangle,
  Bell,
  Hash,
  Mail,
  MessageCircle,
  Phone,
  Send,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { useMemberCoverage, type MemberCoverage } from "@/api/hooks";

/** Icon per contact channel type. Unknown types fall back to a generic bell. */
const CHANNEL_ICONS: Record<string, LucideIcon> = {
  email: Mail,
  phone: Phone,
  whatsapp: MessageCircle,
  telegram: Send,
  webpush: Bell,
  slack_user: Hash,
};

function iconFor(type: string): LucideIcon {
  return CHANNEL_ICONS[type] ?? Bell;
}

/**
 * Renders one member's paging coverage as a row of small channel icons.
 *
 * A channel that is enabled and verified is solid; anything unverified or
 * disabled is muted and dashed, because it cannot page anyone yet. When email
 * is all that is left, an explicit warning badge replaces the quiet-icon story
 * — "we have icons" must never read as "this person is covered".
 */
export function PagingCoverageCell({ coverage }: { coverage?: MemberCoverage }) {
  const { t } = useTranslation("org");

  if (!coverage) {
    return <span className="text-muted-foreground text-xs">—</span>;
  }

  return (
    <div className="flex flex-wrap items-center gap-1.5">
      {coverage.channels.map((channel, index) => {
        const Icon = iconFor(channel.type);
        const live = channel.enabled && channel.verified;
        const label = live
          ? t("members.coverage.channelReady", {
              defaultValue: "{{type}} — ready",
              type: channel.type,
            })
          : t("members.coverage.channelNotReady", {
              defaultValue: "{{type}} — not verified or disabled",
              type: channel.type,
            });

        return (
          <Tooltip key={`${channel.type}-${index}`}>
            <TooltipTrigger asChild>
              <span
                className={cn(
                  "inline-flex h-6 w-6 items-center justify-center rounded border",
                  live
                    ? "border-transparent bg-muted text-foreground"
                    : "border-dashed text-muted-foreground",
                )}
                data-testid={`coverage-channel-${channel.type}`}
                aria-label={label}
              >
                <Icon className="h-3.5 w-3.5" />
              </span>
            </TooltipTrigger>
            <TooltipContent>{label}</TooltipContent>
          </Tooltip>
        );
      })}
      {coverage.emailFallbackOnly && <EmailOnlyBadge />}
    </div>
  );
}

/**
 * The "only reachable by email" warning. Used on the members list and next to
 * any rostered member in the on-call and escalation editors.
 */
export function EmailOnlyBadge({ className }: { className?: string }) {
  const { t } = useTranslation("org");

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Badge
          variant="outline"
          className={cn(
            "gap-1 border-yellow-500/50 text-yellow-700 dark:text-yellow-400",
            className,
          )}
          data-testid="coverage-email-only"
        >
          <AlertTriangle className="h-3 w-3" />
          {t("members.coverage.emailOnly", "Email only")}
        </Badge>
      </TooltipTrigger>
      <TooltipContent className="max-w-xs">
        {t(
          "members.coverage.emailOnlyHelp",
          "No verified phone or messaging contact. Alerts fall back to email, which is not a page at 3am.",
        )}
      </TooltipContent>
    </Tooltip>
  );
}

/**
 * Loads the org's paging coverage and exposes the set of user UIDs that only
 * email can reach. Silently empty for non-admins (the endpoint is admin-only),
 * so callers can render the badge unconditionally without leaking a 403.
 */
export function useEmailOnlyUserUids(org: string, enabled = true) {
  const { data } = useMemberCoverage(org, enabled);

  return useMemo(() => {
    const set = new Set<string>();

    for (const entry of data?.data ?? []) {
      if (entry.emailFallbackOnly) set.add(entry.userUid);
    }

    return set;
  }, [data]);
}

/** Coverage indexed by user UID, for O(1) lookup while rendering a member list. */
export type MemberCoverageMap = Map<string, MemberCoverage>;

/** Builds the lookup map used by member tables. */
export function buildCoverageMap(entries?: MemberCoverage[]): MemberCoverageMap {
  const map: MemberCoverageMap = new Map();

  for (const entry of entries ?? []) {
    map.set(entry.userUid, entry);
  }

  return map;
}
