import type { ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { AlertTriangle, Cpu, User } from "lucide-react";
import type { Event } from "@/api/hooks";
import {
  EventTypeLabel,
  getEventActorName,
  getEventAgentName,
  getEventAgentRegion,
  getEventRegionName,
  getEventChannelName,
  getEventChannelUid,
  getEventCheckName,
  getEventDescription,
  getEventDisconnectReason,
  getEventIntegration,
  getEventLucideIcon,
  getEscalatedDetail,
  getEventPublication,
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

// isAgentConnectionEvent covers the two private-location agent lifecycle
// events (spec 2026-09-25-05) — the only rows that carry agent identity.
function isAgentConnectionEvent(eventType?: string): boolean {
  return eventType === "agent.connected" || eventType === "agent.disconnected";
}

// getEventDetail returns the second line kept under the label (spec
// 2026-09-25-32 proposal step 3, generalised for agent rows by spec
// 2026-09-28-02):
//
// - org.activation.*: a translated description, or — for
//   first_notification_configured specifically — the channel name linked to
//   its integration page.
// - agent.connected / agent.disconnected: `name · region`, plus the
//   translated disconnect reason on a disconnect — the three questions a
//   reader has about an agent row (which agent, which private location, why
//   it dropped), answered from the payload the backend already records.
//
// Returns null when there is nothing to add (every other event type,
// historical rows without the payload keys), so callers can skip rendering
// the wrapping element entirely.
function getEventDetail(
  event: Event,
  org: string,
  t: EventT,
): ReactNode | null {
  if (isAgentConnectionEvent(event.eventType)) {
    const reason =
      event.eventType === "agent.disconnected"
        ? getEventDisconnectReason(event)
        : undefined;
    const parts = [
      getEventAgentName(event),
      getEventAgentRegion(event),
      reason ? t(`disconnectReasons.${reason}`) : undefined,
    ].filter(Boolean);
    return parts.length > 0 ? parts.join(" · ") : null;
  }

  const escalated = getEscalatedDetail(event);
  if (escalated) {
    const parts: ReactNode[] = [];
    if (escalated.stepPosition !== undefined) {
      parts.push(
        <span key="step">{t("descriptions.escalated_step", { position: escalated.stepPosition })}</span>,
      );
    }
    if (escalated.targets.length > 0) {
      escalated.targets.forEach((target, i) => {
        const label =
          target.type === "all_admins"
            ? t("descriptions.escalated_all_admins")
            : target.name ?? target.type;
        let node: ReactNode = <span>{label}</span>;
        if (target.type === "integration" && target.uid) {
          node = (
            <Link
              to="/orgs/$org/integrations/$integrationUid"
              params={{ org, integrationUid: target.uid }}
              className="text-primary hover:underline"
              data-testid="event-detail-escalated-integration-link"
            >
              {label}
            </Link>
          );
        } else if (target.type === "schedule" && target.uid) {
          node = (
            <Link
              to="/orgs/$org/on-call/$uid"
              params={{ org, uid: target.uid }}
              className="text-primary hover:underline"
              data-testid="event-detail-escalated-schedule-link"
            >
              {label}
            </Link>
          );
        }
        parts.push(
          <span key={`t${i}`}>
            {node}
            {target.type === "schedule" && target.onCallUserName
              ? ` (${target.onCallUserName})`
              : null}
          </span>,
        );
      });
    } else if (escalated.policyName) {
      parts.push(<span key="policy">{escalated.policyName}</span>);
    }
    if (parts.length > 0) {
      return (
        <>
          {parts.map((part, i) => (
            <span key={i}>
              {i > 0 ? (i === 1 && escalated.stepPosition !== undefined ? " · " : ", ") : null}
              {part}
            </span>
          ))}
        </>
      );
    }
  }

  const regionName = getEventRegionName(event);
  if (regionName) return regionName;

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

  // integration.*: which integration was created / updated / deleted, linked
  // to its page while it still exists (solidping-business#28).
  const integration = getEventIntegration(event);
  if (integration) {
    return integration.uid ? (
      <Link
        to="/orgs/$org/integrations/$integrationUid"
        params={{ org, integrationUid: integration.uid }}
        className="text-primary hover:underline"
        data-testid="event-detail-integration-link"
      >
        {integration.name}
      </Link>
    ) : (
      <span>{integration.name}</span>
    );
  }

  // statuspage.incident.*: what was published or resolved on the status page,
  // linked to the incident behind it (solidping-business#30, #31).
  const publication = getEventPublication(event);
  if (publication) {
    return publication.incidentUid ? (
      <Link
        to="/orgs/$org/incidents/$incidentUid"
        params={{ org, incidentUid: publication.incidentUid }}
        className="text-primary hover:underline"
        data-testid="event-detail-publication-link"
      >
        {publication.title}
      </Link>
    ) : (
      <span>{publication.title}</span>
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
          {/* w-px shrinks the column to its content ("2h ago"). Below md the
              time folds into the Event cell to leave room for Related. */}
          <TableHead className="hidden w-px whitespace-nowrap md:table-cell">
            {t("table.time")}
          </TableHead>
          <TableHead className="md:w-[220px]">{t("table.event")}</TableHead>
          <TableHead className={cn("w-[120px]", embedded && "hidden md:table-cell")}>
            {t("table.actor")}
          </TableHead>
          <TableHead>{t("table.related")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {events.map((event) => {
          const eventDetail = getEventDetail(event, org, t);
          // The Related chip names the agent (falling back to its region,
          // then to nothing — today's empty cell) and opens the Private
          // Locations page, the agent's only detail surface.
          const agentLabel = isAgentConnectionEvent(event.eventType)
            ? getEventAgentName(event) ?? getEventAgentRegion(event)
            : undefined;
          const AgentIcon = getEventLucideIcon(event.eventType);

          const stripe = getEventRowStripe(event.eventType);
          // Relative reads faster when scanning a log; the exact timestamp
          // stays one hover away.
          const time = event.createdAt ? (
            <span title={new Date(event.createdAt).toLocaleString()}>
              <DurationAgo since={event.createdAt} />
            </span>
          ) : (
            "—"
          );

          return (
            <TableRow
              key={event.uid}
              className="transition-colors hover:bg-muted/40"
            >
              <TableCell
                className={cn(
                  "hidden whitespace-nowrap text-xs tabular-nums text-muted-foreground md:table-cell",
                  stripe,
                )}
                data-testid="event-time"
              >
                {time}
              </TableCell>
              {/* Below md the Time column is hidden: the Event cell carries
                  the row stripe and the time, under the label. */}
              <TableCell className={cn(stripe, stripe && "md:shadow-none")}>
                <EventTypeLabel eventType={event.eventType} t={t} />
                <div
                  className="pl-6 text-xs tabular-nums text-muted-foreground md:hidden"
                  data-testid="event-time-inline"
                >
                  {time}
                </div>
                {eventDetail ? (
                  // Indented to align under the label text rather than the
                  // icon: size-4 (16px) + gap-2 (8px) = 24px = pl-6.
                  <div className="pl-6 text-xs text-muted-foreground truncate" data-testid="event-detail">
                    {eventDetail}
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
                  {agentLabel && (
                    <Link
                      to="/orgs/$org/organization/private-locations"
                      params={{ org }}
                      title={t("links.privateLocation")}
                      className="inline-flex items-center gap-1.5 text-xs font-medium text-foreground transition-colors hover:text-primary hover:underline"
                    >
                      <AgentIcon className="h-3 w-3 shrink-0 text-muted-foreground/70" />
                      {agentLabel}
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
