import { useTranslation } from "react-i18next";
import { AlertTriangle } from "lucide-react";
import {
  useIncident,
  useIncidents,
  useTracerouteCapture,
  type IncidentAttachment,
  type IncidentDetail,
  type TracerouteCapture,
  type TracerouteHop,
} from "@/api/hooks";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { cn } from "@/lib/utils";

// TracerouteCard renders the MTR-style path capture taken when a check went
// down on a network-reachability failure (spec 2026-08-21-10).
//
// IT IS DELIBERATELY LOUD ABOUT WHAT THE CAPTURE CAN AND CANNOT SEE. The three
// probe modes observe genuinely different things: the two ICMP modes name every
// router that answered, while the TCP fallback cannot see intermediate hop
// addresses at all and only reports how far the SYN got. Rendering the second
// as a column of blanks — indistinguishable from "the routers stayed silent" —
// would invite an operator to conclude the path is broken where it is merely
// unobservable. The mode badge and the notice below exist for that reason and
// are not decoration.
export function IncidentTracerouteCard({
  incident,
}: {
  incident: IncidentDetail;
}) {
  const { t } = useTranslation("incidents");

  const attachment: IncidentAttachment | undefined = (
    incident.attachments ?? []
  ).find((a) => a.kind === "traceroute" && a.downloadUrl);

  const { data, isLoading, isError } = useTracerouteCapture(
    attachment?.downloadUrl,
  );

  if (!attachment) return null;

  return (
    <Card data-testid="incident-traceroute-card">
      <CardHeader>
        <CardTitle>{t("detail.traceroute.title")}</CardTitle>
        <CardDescription>{t("detail.traceroute.description")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {isLoading && <Skeleton className="h-32 w-full" />}

        {isError && (
          <Alert variant="destructive" data-testid="incident-traceroute-error">
            <AlertTriangle className="h-4 w-4" />
            <AlertDescription>
              {t("detail.traceroute.loadFailed")}
            </AlertDescription>
          </Alert>
        )}

        {data && <TracerouteBody capture={data} />}
      </CardContent>
    </Card>
  );
}

function TracerouteBody({ capture }: { capture: TracerouteCapture }) {
  const { t } = useTranslation("incidents");

  return (
    <div className="space-y-4">
      <div
        className="flex flex-wrap items-center gap-2"
        data-testid="incident-traceroute-meta"
      >
        <Badge variant="outline" data-testid="incident-traceroute-mode">
          {t(`detail.traceroute.modes.${capture.mode}`, {
            defaultValue: capture.mode,
          })}
        </Badge>
        <Badge variant="outline">
          {t("detail.traceroute.target", {
            target: capture.host
              ? `${capture.host} (${capture.address})`
              : capture.address,
          })}
        </Badge>
        {capture.region && (
          <Badge variant="outline" data-testid="incident-traceroute-region">
            {t("detail.traceroute.region", { region: capture.region })}
          </Badge>
        )}
        <Badge variant="outline">
          {t("detail.traceroute.rounds", { rounds: capture.rounds })}
        </Badge>
      </div>

      {/* The honesty notice. `hopAddressesVisible` is false only for the TCP
          fallback, where an empty address column means "we could not hear the
          routers", not "the routers did not answer". */}
      {!capture.hopAddressesVisible && (
        <Alert data-testid="incident-traceroute-blind-notice">
          <AlertTriangle className="h-4 w-4" />
          <AlertDescription>
            {t("detail.traceroute.tcpModeNotice")}
          </AlertDescription>
        </Alert>
      )}

      {!capture.complete && (
        <Alert data-testid="incident-traceroute-incomplete">
          <AlertTriangle className="h-4 w-4" />
          <AlertDescription>
            {capture.truncated
              ? t("detail.traceroute.truncated")
              : t("detail.traceroute.incomplete")}
          </AlertDescription>
        </Alert>
      )}

      {/* Wide content scrolls INSIDE its own container: the incident page must
          never scroll horizontally on a phone. */}
      <div className="overflow-x-auto">
        <Table data-testid="incident-traceroute-table">
          <TableHeader>
            <TableRow>
              <TableHead className="w-12">
                {t("detail.traceroute.columns.hop")}
              </TableHead>
              <TableHead>{t("detail.traceroute.columns.host")}</TableHead>
              <TableHead className="text-right">
                {t("detail.traceroute.columns.loss")}
              </TableHead>
              <TableHead className="text-right">
                {t("detail.traceroute.columns.avg")}
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {capture.hops.map((hop) => (
              <HopRow
                key={hop.ttl}
                hop={hop}
                addressesVisible={capture.hopAddressesVisible}
              />
            ))}
          </TableBody>
        </Table>
      </div>

      <p
        className="text-xs text-muted-foreground"
        data-testid="incident-traceroute-caption"
      >
        {t("detail.traceroute.caption", {
          startedAt: new Date(capture.startedAt).toLocaleString(),
          durationMs: capture.durationMs,
        })}
      </p>
    </div>
  );
}

function HopRow({
  hop,
  addressesVisible,
}: {
  hop: TracerouteHop;
  addressesVisible: boolean;
}) {
  const { t } = useTranslation("incidents");

  const lost = hop.lossPct >= 100;

  return (
    <TableRow data-testid={`incident-traceroute-hop-${hop.ttl}`}>
      <TableCell className="font-mono text-xs">{hop.ttl}</TableCell>
      <TableCell className="font-mono text-xs break-all">
        {hop.address ? (
          <span>
            {hop.hostname ? (
              <>
                {hop.hostname}{" "}
                <span className="text-muted-foreground">({hop.address})</span>
              </>
            ) : (
              hop.address
            )}
            {hop.addresses && hop.addresses.length > 1 && (
              <span className="text-muted-foreground">
                {" "}
                {t("detail.traceroute.extraPaths", {
                  count: hop.addresses.length - 1,
                })}
              </span>
            )}
            {hop.final && (
              <Badge variant="outline" className="ml-2">
                {t("detail.traceroute.targetBadge")}
              </Badge>
            )}
            {hop.unreachable && (
              <Badge variant="destructive" className="ml-2">
                {t("detail.traceroute.unreachableBadge")}
              </Badge>
            )}
          </span>
        ) : (
          <span className="text-muted-foreground">
            {addressesVisible
              ? t("detail.traceroute.noReply")
              : t("detail.traceroute.notObservable")}
          </span>
        )}
      </TableCell>
      <TableCell
        className={cn(
          "text-right font-mono text-xs",
          lost && "text-destructive",
        )}
      >
        {t("detail.traceroute.lossValue", { pct: hop.lossPct })}
        <span className="ml-1 text-muted-foreground">
          ({hop.received}/{hop.sent})
        </span>
      </TableCell>
      <TableCell className="text-right font-mono text-xs">
        {hop.rttAvgMs === undefined
          ? "—"
          : t("detail.traceroute.msValue", { ms: hop.rttAvgMs })}
      </TableCell>
    </TableRow>
  );
}

// ResultTracerouteCard renders the path capture on a RESULT detail page.
//
// A trace is stored against the INCIDENT, not the result, because that is the
// only place it is bounded — one capture per outage rather than one per failing
// probe. So the link runs the other way here: given a result, find the incident
// this result opened (or reopened), and render its capture.
//
// The lookup is client-side and cheap by construction. It is gated on `enabled`
// — the result page passes false for a passing probe and for every aggregated
// row — so the extra query only ever fires on the handful of pages where a
// trace could possibly exist. There is no server-side "incident by onset
// result" index, and adding one to serve a rarely-visited page would be a
// migration in search of a problem.
export function ResultTracerouteCard({
  org,
  checkUid,
  resultUid,
  enabled,
}: {
  org: string;
  checkUid: string;
  resultUid: string;
  enabled: boolean;
}) {
  const { data: incidents } = useIncidents(org, {
    checkUid,
    size: 25,
    enabled: enabled && !!checkUid,
  });

  // Both onsets count: `first_result` is the incident's open, `last_failure`
  // the most recent relapse, and the stored capture belongs to whichever of
  // them is current.
  const match = (incidents?.data ?? []).find(
    (incident) =>
      incident.details?.first_result?.resultUid === resultUid ||
      incident.details?.last_failure?.resultUid === resultUid,
  );

  // Only the DETAIL endpoint populates `attachments`, so the list row above is
  // just the lookup — the capture itself needs this second fetch.
  const { data: incident } = useIncident(org, match?.uid ?? "");

  if (!incident) return null;

  return <IncidentTracerouteCard incident={incident} />;
}
