import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { RefreshCw, Search, ShieldCheck } from "lucide-react";
import { useAuditEvents, useMembers, type Event } from "@/api/hooks";
import { EventTypeBadge } from "@/components/dashboard/event-display";
import { QueryErrorView } from "@/components/shared/error-views";
import { useAuth } from "@/contexts/AuthContext";
import { DurationAgo } from "@/components/shared/relative-time";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

/**
 * Org-level audit log (spec 2026-08-21-09).
 *
 * Admin-gated by its PARENT: /orgs/$org/organization already bounces
 * non-admins, so this route inherits the gate rather than re-implementing it.
 * The real enforcement is server-side regardless — the API refuses to return
 * `auth.*` events, source IPs and user agents to a non-admin, so hiding the
 * page is convenience, never the security boundary.
 */

/** The families the filter offers, in the order an access review reads them:
 *  who got in, who is a member, then what they changed. */
const FAMILIES = [
  "auth",
  "member",
  "integration",
  "escalation_policy",
  "oncall_schedule",
  "status_page",
  "maintenance_window",
  "config",
  "check",
  "incident",
] as const;

type Family = (typeof FAMILIES)[number];

/** The object kinds the backend writes as `target_type`, in the order the
 *  families above list them. */
const TARGET_TYPES = [
  "user",
  "member",
  "token",
  "agent_enrollment_token",
  "agent_key",
  "integration",
  "escalation_policy",
  "oncall_schedule",
  "status_page",
  "maintenance_window",
  "check",
  "manifest",
  "organization",
] as const;

type TargetType = (typeof TARGET_TYPES)[number];

const RANGES = ["24h", "7d", "30d", "90d", "all"] as const;
type Range = (typeof RANGES)[number];

const RANGE_HOURS: Record<Range, number | null> = {
  "24h": 24,
  "7d": 24 * 7,
  "30d": 24 * 30,
  "90d": 24 * 90,
  all: null,
};

const PAGE_SIZE = 50;

/** How long a typed filter settles before it reaches the URL (and the API). */
const FILTER_DEBOUNCE_MS = 400;

interface AuditSearch {
  family?: Family;
  actor?: string;
  range?: Range;
  targetType?: TargetType;
  target?: string;
  ip?: string;
}

export const Route = createFileRoute("/orgs/$org/organization/audit")({
  component: AuditPage,
  // The filters ARE this page — they decide what it is showing — so they live
  // in the URL rather than useState: an audit finding is something people
  // paste to each other, and a link that reproduces the exact view is the
  // difference between "look at row 40" and "look at the audit page, sort of".
  validateSearch: (search: Record<string, unknown>): AuditSearch => {
    const out: AuditSearch = {};

    if (
      typeof search.family === "string" &&
      (FAMILIES as readonly string[]).includes(search.family)
    ) {
      out.family = search.family as Family;
    }

    if (typeof search.actor === "string" && search.actor !== "") {
      out.actor = search.actor;
    }

    if (
      typeof search.range === "string" &&
      (RANGES as readonly string[]).includes(search.range)
    ) {
      out.range = search.range as Range;
    }

    if (
      typeof search.targetType === "string" &&
      (TARGET_TYPES as readonly string[]).includes(search.targetType)
    ) {
      out.targetType = search.targetType as TargetType;
    }

    if (typeof search.target === "string" && search.target !== "") {
      out.target = search.target;
    }

    if (typeof search.ip === "string" && search.ip !== "") {
      out.ip = search.ip;
    }

    return out;
  },
});

/** actorLabel renders who caused an event: the person's name, else their
 *  email, else the bare UID, else "System". Never an empty cell — "nobody"
 *  and "someone we cannot name" are different facts in an audit trail. */
function actorLabel(event: Event, systemLabel: string): string {
  if (event.actorName) return event.actorName;
  if (event.actorEmail) return event.actorEmail;
  if (event.actorUid) return event.actorUid;
  return systemLabel;
}

/** targetLabel pulls the human-readable target out of the redacted payload the
 *  backend wrote (target_name / target_uid), falling back to the target type
 *  alone for events that name no specific object. */
function targetLabel(event: Event): string {
  const payload = event.payload ?? {};
  const name = payload.target_name;
  if (typeof name === "string" && name !== "") return name;

  const uid = payload.target_uid;
  if (typeof uid === "string" && uid !== "") return uid;

  const type = payload.target_type;
  if (typeof type === "string" && type !== "") return type;

  return "—";
}

/** changedFields renders the `changed_fields` list update events carry. It is
 *  the whole point of the redaction rule: the trail says WHICH fields moved,
 *  and for sensitive ones that is all it will ever say. */
function changedFields(event: Event): string[] {
  const fields = event.payload?.changed_fields;
  if (!Array.isArray(fields)) return [];
  return fields.filter((field): field is string => typeof field === "string");
}

function AuditPage() {
  const { t } = useTranslation(["events", "common"]);
  const { org } = Route.useParams();
  const { family, actor, range, targetType, target, ip } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });

  const activeRange: Range = range ?? "7d";

  // Cursor is deliberately NOT in the URL: a pasted link should reproduce the
  // filters, not a position halfway down a trail that has grown since.
  const [cursors, setCursors] = useState<string[]>([]);
  const cursor = cursors[cursors.length - 1];

  const { data, isLoading, error, refetch, isRefetching } = useAuditEvents(org, {
    family,
    actorUserUid: actor,
    targetType,
    target,
    sourceIp: ip,
    sinceHours: RANGE_HOURS[activeRange] ?? undefined,
    cursor,
    size: PAGE_SIZE,
  });

  const { user } = useAuth();
  const { data: membersData } = useMembers(org);
  const members = membersData?.data ?? [];

  // Any filter change resets pagination — keeping a cursor from the previous
  // filter set would page into a result list that no longer exists.
  const setFilters = (next: Partial<AuditSearch>) => {
    setCursors([]);
    navigate({
      to: ".",
      search: (prev) => ({ ...prev, ...next }),
      replace: true,
    });
  };

  // The two free-text filters are typed, so the URL is written on a debounce
  // rather than on every keystroke — a keystroke-per-request audit query would
  // hammer an endpoint that scans a large table.
  //
  // The debounce is a timer started from the CHANGE HANDLER, not an effect
  // watching a debounced value: the effect form has to call setState during
  // render-phase reconciliation, which React (and the lint rule) rightly
  // objects to. The only effect here clears pending timers on unmount.
  //
  // Drafts are seeded from the URL once, at mount, so a shared link renders
  // with its filters visible in the inputs (the search params alone do not
  // survive a cold deep-link into a layout route).
  const [targetDraft, setTargetDraft] = useState(target ?? "");
  const [ipDraft, setIpDraft] = useState(ip ?? "");

  const commitTimers = useRef<Record<string, ReturnType<typeof setTimeout>>>({});

  useEffect(
    () => () => {
      for (const timer of Object.values(commitTimers.current)) {
        clearTimeout(timer);
      }
    },
    [],
  );

  const commitFilter = (key: "target" | "ip", value: string) => {
    clearTimeout(commitTimers.current[key]);

    commitTimers.current[key] = setTimeout(() => {
      const next = value.trim();
      setFilters({ [key]: next === "" ? undefined : next });
    }, FILTER_DEBOUNCE_MS);
  };

  const events = data?.data ?? [];

  return (
    <div className="space-y-4" data-testid="audit-page">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex items-start gap-2">
          <ShieldCheck className="mt-0.5 h-5 w-5 shrink-0 text-muted-foreground" />
          <div>
            <h2 className="text-lg font-semibold">{t("events:audit.title")}</h2>
            <p className="text-sm text-muted-foreground">
              {t("events:audit.subtitle")}
            </p>
          </div>
        </div>
        <Button
          variant="outline"
          onClick={() => refetch()}
          disabled={isRefetching}
          aria-label={t("common:refresh")}
        >
          <RefreshCw
            className={`h-4 w-4 sm:mr-2 ${isRefetching ? "animate-spin" : ""}`}
          />
          <span className="hidden sm:inline">{t("common:refresh")}</span>
        </Button>
      </div>

      <div className="flex flex-wrap gap-2">
        <Select
          value={family ?? "all"}
          onValueChange={(value) =>
            setFilters({ family: value === "all" ? undefined : (value as Family) })
          }
        >
          <SelectTrigger
            className="w-full sm:w-[220px]"
            data-testid="audit-family-filter"
          >
            <SelectValue placeholder={t("events:audit.filters.family")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">
              {t("events:audit.filters.allFamilies")}
            </SelectItem>
            {FAMILIES.map((value) => (
              <SelectItem key={value} value={value}>
                {t(`events:audit.families.${value}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Select
          value={actor ?? "all"}
          onValueChange={(value) =>
            setFilters({ actor: value === "all" ? undefined : value })
          }
        >
          <SelectTrigger
            className="w-full sm:w-[220px]"
            data-testid="audit-actor-filter"
          >
            <SelectValue placeholder={t("events:audit.filters.actor")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">
              {t("events:audit.filters.allActors")}
            </SelectItem>
            {members.map((member) => (
              <SelectItem key={member.userUid} value={member.userUid}>
                {member.name || member.email}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Select
          value={activeRange}
          onValueChange={(value) => setFilters({ range: value as Range })}
        >
          <SelectTrigger
            className="w-full sm:w-[180px]"
            data-testid="audit-range-filter"
          >
            <SelectValue placeholder={t("events:audit.filters.range")} />
          </SelectTrigger>
          <SelectContent>
            {RANGES.map((value) => (
              <SelectItem key={value} value={value}>
                {t(`events:audit.ranges.${value}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Select
          value={targetType ?? "all"}
          onValueChange={(value) =>
            setFilters({
              targetType: value === "all" ? undefined : (value as TargetType),
            })
          }
        >
          <SelectTrigger
            className="w-full sm:w-[200px]"
            data-testid="audit-target-type-filter"
          >
            <SelectValue placeholder={t("events:audit.filters.targetType")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">
              {t("events:audit.filters.allTargetTypes")}
            </SelectItem>
            {TARGET_TYPES.map((value) => (
              <SelectItem key={value} value={value}>
                {t(`events:audit.targetTypes.${value}`, { defaultValue: value })}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <div className="relative w-full sm:w-[220px]">
          <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            className="pl-9"
            placeholder={t("events:audit.filters.target")}
            value={targetDraft}
            onChange={(event) => {
              setTargetDraft(event.target.value);
              commitFilter("target", event.target.value);
            }}
            data-testid="audit-target-filter"
          />
        </div>

        {/* The IP filter is offered to admins only, mirroring the API: a
            non-admin's sourceIp parameter is ignored server-side, so showing
            the control would be a promise the backend deliberately breaks. */}
        {user?.isAdmin && (
          <div className="relative w-full sm:w-[180px]">
            <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              className="pl-9 font-mono text-xs"
              placeholder={t("events:audit.filters.ip")}
              value={ipDraft}
              onChange={(event) => {
                setIpDraft(event.target.value);
                commitFilter("ip", event.target.value);
              }}
              data-testid="audit-ip-filter"
            />
          </div>
        )}
      </div>

      {error ? (
        <QueryErrorView error={error} org={org} onRetry={() => refetch()} />
      ) : isLoading ? (
        <div className="space-y-2">
          {[...Array(8)].map((_, index) => (
            <Skeleton key={index} className="h-12 rounded-lg" />
          ))}
        </div>
      ) : (
        <>
          {/* The table scrolls inside its own border rather than pushing the
              page sideways, so a 375px viewport keeps a usable layout. */}
          <div className="overflow-x-auto rounded-md border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="whitespace-nowrap">
                    {t("events:audit.columns.time")}
                  </TableHead>
                  <TableHead className="whitespace-nowrap">
                    {t("events:audit.columns.event")}
                  </TableHead>
                  <TableHead className="whitespace-nowrap">
                    {t("events:audit.columns.actor")}
                  </TableHead>
                  <TableHead>{t("events:audit.columns.target")}</TableHead>
                  {/* The IP is the least-often-read column and the first to go
                      on a narrow viewport. It is also absent entirely for
                      deployments that turned capture off. */}
                  <TableHead className="hidden whitespace-nowrap lg:table-cell">
                    {t("events:audit.columns.ip")}
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {events.length === 0 ? (
                  <TableRow>
                    <TableCell
                      colSpan={5}
                      className="py-8 text-center text-sm text-muted-foreground"
                      data-testid="audit-empty"
                    >
                      {t("events:audit.empty")}
                    </TableCell>
                  </TableRow>
                ) : (
                  events.map((event) => (
                    <TableRow key={event.uid} data-testid="audit-row">
                      <TableCell
                        className="whitespace-nowrap text-sm text-muted-foreground"
                        title={event.createdAt}
                      >
                        {event.createdAt ? (
                          <DurationAgo since={event.createdAt} />
                        ) : (
                          "—"
                        )}
                      </TableCell>
                      <TableCell className="whitespace-nowrap">
                        <EventTypeBadge eventType={event.eventType} t={t} />
                      </TableCell>
                      <TableCell className="max-w-0">
                        <div
                          className="truncate text-sm"
                          title={actorLabel(event, t("events:audit.system"))}
                        >
                          {actorLabel(event, t("events:audit.system"))}
                        </div>
                        {event.actorType && event.actorType !== "user" && (
                          <div className="text-xs text-muted-foreground">
                            {t(`events:actorTypes.${event.actorType}`, {
                              defaultValue: event.actorType,
                            })}
                          </div>
                        )}
                      </TableCell>
                      <TableCell className="max-w-0">
                        <div
                          className="truncate text-sm"
                          title={targetLabel(event)}
                        >
                          {targetLabel(event)}
                        </div>
                        {changedFields(event).length > 0 && (
                          <div className="mt-1 flex flex-wrap gap-1">
                            {changedFields(event).map((field) => (
                              <Badge
                                key={field}
                                variant="outline"
                                className="text-[10px] font-normal"
                              >
                                {field}
                              </Badge>
                            ))}
                          </div>
                        )}
                      </TableCell>
                      <TableCell className="hidden whitespace-nowrap font-mono text-xs text-muted-foreground lg:table-cell">
                        {event.sourceIp ?? "—"}
                      </TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          </div>

          {data?.cursor && (
            <div className="flex justify-center">
              <Button
                variant="outline"
                onClick={() => setCursors((prev) => [...prev, data.cursor!])}
                data-testid="audit-load-more"
              >
                {t("events:audit.loadMore")}
              </Button>
            </div>
          )}
        </>
      )}
    </div>
  );
}
