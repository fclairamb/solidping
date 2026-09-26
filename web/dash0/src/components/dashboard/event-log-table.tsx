import type { ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { AlertTriangle, Cpu, User } from "lucide-react";
import type { Event } from "@/api/hooks";
import {
  EventTypeLabel,
  getEventActorName,
  getEventChannelName,
  getEventChannelUid,
  getEventCheckName,
  getEventDescription,
  getEventRowStripe,
} from "@/components/dashboard/event-display";
import { DurationAgo } from "@/components/shared/relative-time";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { cn } from "@/lib/utils";

type EventT = (key: string, options?: Record<string, unknown>) => string;

interface EventLogTableProps {
  org: string;
  events: Event[];
  t: EventT;
  /**
   * "standalone" (default) is the Events page: its own bordered/shadowed
   * surface, and the table already scrolls horizontally at narrow widths so
   * every column, including Actor, always renders.
   *
   * "embedded" is the dashboard's "Recent activity" card: no outer
   * border/shadow (the parent Card already draws one), and the Actor column
   * hides below the `md` breakpoint so the card does not get wider than its
   * siblings on a laptop screen. Spec 2026-09-25-32's resolved open question
   * is binding here: hide the column responsively, never drop it — it is
   * still there, and still visible, at `md` and above.
   */
  variant?: "standalone" | "embedded";
}

// getActivationDetail returns the second line kept for org.activation.*
// events (spec 2026-09-25-32 proposal step 3): a translated description, or
// — for first_notification_configured specifically — the channel name linked
// to its integration page. Returns null for every other event type (which is
// every event carrying a checkUid/incidentUid instead), so callers can skip
// rendering the wrapping element entirely.
function getActivationDetail(
  event: Event,
  org: string,
  t: EventT,
): ReactNode | null {
  const channelName =
    event.eventType === "org.activation.first_notification_configured"
      ? getEventChannelName(event)
      : undefined;

  if (channelName) {
    const channelUid = getEventChannelUid(event);
    return (
      <>
        {t("descriptions.first_notification_configured_prefix")}{" "}
        {channelUid ? (
          <Link
            to="/orgs/$org/integrations/$integrationUid"
            params={{ org, integrationUid: channelUid }}
            className="text-primary hover:underline"
          >
            {channelName}
          </Link>
        ) : (
          <span>{channelName}</span>
        )}
      </>
    );
  }

  const description = getEventDescription(event.eventType, t);
  return description ? description : null;
}

// EventLogTable is the ONE rendering of an events table, shared by the
// Events page and the dashboard's "Recent activity" card (spec
// 2026-09-25-32) so the two can no longer drift apart the way they did
// before: same EventTypeLabel icon, same muted/foreground/bold tone rules,
// same DurationAgo + hover timestamp, same actor cell, same related
// check/incident links and the same getEventRowStripe leading edge.
export function EventLogTable({
  org,
  events,
  t,
  variant = "standalone",
}: EventLogTableProps) {
  const embedded = variant === "embedded";

  const table = (
    <Table>
      <TableHeader className="bg-muted/30">
        <TableRow>
          {/* w-px shrinks the column to its content ("2h ago"). */}
          <TableHead className="w-px whitespace-nowrap">
            {t("table.time")}
          </TableHead>
          <TableHead className="w-[220px]">{t("table.event")}</TableHead>
          <TableHead className={cn("w-[120px]", embedded && "hidden md:table-cell")}>
            {t("table.actor")}
          </TableHead>
          <TableHead>{t("table.related")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {events.map((event) => {
          const activationDetail = getActivationDetail(event, org, t);

          return (
            <TableRow
              key={event.uid}
              className="transition-colors hover:bg-muted/40"
            >
              <TableCell
                className={cn(
                  "whitespace-nowrap text-xs tabular-nums text-muted-foreground",
                  getEventRowStripe(event.eventType),
                )}
              >
                {event.createdAt ? (
                  // Relative reads faster when scanning a log; the exact
                  // timestamp stays one hover away.
                  <span title={new Date(event.createdAt).toLocaleString()}>
                    <DurationAgo since={event.createdAt} />
                  </span>
                ) : (
                  "—"
                )}
              </TableCell>
              <TableCell>
                <EventTypeLabel eventType={event.eventType} t={t} />
                {activationDetail ? (
                  // Indented to align under the label text rather than the
                  // icon: size-4 (16px) + gap-2 (8px) = 24px = pl-6.
                  <div className="pl-6 text-xs text-muted-foreground truncate">
                    {activationDetail}
                  </div>
                ) : null}
              </TableCell>
              <TableCell className={cn(embedded && "hidden md:table-cell")}>
                <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
                  {event.actorType === "user" ? (
                    <User className="h-3 w-3 shrink-0 text-muted-foreground/70" />
                  ) : (
                    <Cpu className="h-3 w-3 shrink-0 text-muted-foreground/70" />
                  )}
                  {/* The NAME when the event carries one — including a
                      Slack/Discord/phone acker, who has no users row
                      and would otherwise show as the bare word "user".
                      Falls back to the localized actor type, which is
                      the only thing capitalized: capitalizing a name
                      or an email mangles it. */}
                  {getEventActorName(event) ?? (
                    <span className="capitalize">
                      {t(`actorTypes.${event.actorType || "system"}`)}
                    </span>
                  )}
                </span>
              </TableCell>
              <TableCell>
                <div className="flex flex-wrap items-center gap-2">
                  {event.checkUid && (
                    <Link
                      to="/orgs/$org/checks/$checkUid"
                      params={{ org, checkUid: event.checkUid }}
                      search={{
                        graphPeriod: undefined,
                        graphFull: undefined,
                        region: undefined,
                      }}
                      className="inline-flex items-center gap-1.5 text-xs font-medium text-foreground transition-colors hover:text-primary hover:underline"
                    >
                      <Cpu className="h-3 w-3 shrink-0 text-muted-foreground/70" />
                      {getEventCheckName(event) ?? t("links.check")}
                    </Link>
                  )}
                  {event.incidentUid && (
                    <Link
                      to="/orgs/$org/incidents/$incidentUid"
                      params={{ org, incidentUid: event.incidentUid }}
                      className="inline-flex items-center gap-1 rounded border bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground transition-colors hover:text-foreground"
                    >
                      <AlertTriangle className="h-3 w-3 shrink-0" />
                      {t("links.incident")}
                    </Link>
                  )}
                </div>
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );

  if (embedded) {
    return <div className="overflow-x-auto">{table}</div>;
  }

  return (
    <div className="overflow-hidden rounded-xl border bg-card shadow-card">
      <div className="overflow-x-auto">{table}</div>
    </div>
  );
}
