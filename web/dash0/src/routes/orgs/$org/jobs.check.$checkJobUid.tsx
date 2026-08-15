import { useTranslation } from "react-i18next";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { Activity, ArrowLeft } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { TimeAgoOrDash } from "@/components/ui/time-ago";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { JsonViewer } from "@/components/shared/json-viewer";
import { useCheckJob, useRegions, type CheckJobState } from "@/api/hooks";
import { regionDisplayLabel } from "@/lib/region-label";

interface CheckJobDetailSearch {
  allOrgs?: boolean;
}

export const Route = createFileRoute("/orgs/$org/jobs/check/$checkJobUid")({
  component: CheckJobDetailPage,
  validateSearch: (search: Record<string, unknown>): CheckJobDetailSearch => ({
    allOrgs: search.allOrgs === true || search.allOrgs === "true",
  }),
});

type BadgeVariant = "default" | "secondary" | "destructive" | "outline" | "success" | "warning";

function checkStateVariant(state: CheckJobState): BadgeVariant {
  switch (state) {
    case "inFlight":
      return "default";
    case "idle":
      return "secondary";
    case "stalled":
      return "warning";
    case "crashLooping":
      return "destructive";
    default:
      return "outline";
  }
}

function formatPeriod(seconds: number): string {
  if (seconds <= 0) return "—";
  if (seconds % 3600 === 0) return `${seconds / 3600}h`;
  if (seconds % 60 === 0) return `${seconds / 60}m`;
  return `${seconds}s`;
}

function MetaRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5 sm:flex-row sm:gap-2">
      <dt className="w-40 shrink-0 text-sm text-muted-foreground">{label}</dt>
      <dd className="text-sm">{children}</dd>
    </div>
  );
}

function CheckJobDetailPage() {
  const { t } = useTranslation("jobs");
  const { org, checkJobUid } = Route.useParams();
  const { allOrgs } = Route.useSearch();

  const scope = { allOrgs: allOrgs ?? false };
  const { data: row, isLoading, isError } = useCheckJob(org, checkJobUid, scope);
  // The job row carries the raw region slug; show the friendly
  // "{emoji} {name}" label from the region definitions (raw-slug fallback).
  const { data: regionsData } = useRegions(org);

  if (isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-10 w-64" />
        <Skeleton className="h-40 w-full" />
      </div>
    );
  }

  if (isError || !row) {
    return (
      <div className="space-y-4">
        <BackButton org={org} allOrgs={scope.allOrgs} />
        <p className="text-sm text-muted-foreground">{t("notFound")}</p>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex items-start gap-3">
        <BackButton org={org} allOrgs={scope.allOrgs} />
        <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-md bg-muted">
          <Activity className="h-5 w-5" />
        </div>
        <div className="min-w-0 flex-1">
          <h1 className="flex items-center gap-2 text-2xl font-semibold tracking-tight">
            <span className="truncate">{row.checkName ?? row.checkUid}</span>
            <Badge variant={checkStateVariant(row.state)}>
              {t(`state.${row.state}`, row.state)}
            </Badge>
          </h1>
          <p className="mt-1 text-sm text-muted-foreground">{t("detail.checkJobTitle")}</p>
        </div>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t("detail.meta")}</CardTitle>
        </CardHeader>
        <CardContent>
          <dl className="space-y-2">
            <MetaRow label={t("columns.check")}>
              <Link
                to="/orgs/$org/checks/$checkUid"
                params={{ org, checkUid: row.checkUid }}
                search={{ graphPeriod: undefined, graphFull: undefined, region: undefined }}
                className="hover:underline"
              >
                {row.checkName ?? row.checkUid}
              </Link>
            </MetaRow>
            <MetaRow label={t("columns.region")}>
              {row.region
                ? regionDisplayLabel(regionsData?.regions, row.region)
                : t("labels.none")}
            </MetaRow>
            <MetaRow label={t("columns.type")}>{row.type}</MetaRow>
            <MetaRow label={t("columns.period")}>{formatPeriod(row.periodSeconds)}</MetaRow>
            <MetaRow label={t("columns.nextRun")}>
              <TimeAgoOrDash date={row.scheduledAt} data-testid="check-schedule-next-run" />
            </MetaRow>
            <MetaRow label={t("columns.state")}>
              <Badge variant={checkStateVariant(row.state)}>
                {t(`state.${row.state}`, row.state)}
              </Badge>
            </MetaRow>
            <MetaRow label={t("columns.leaseWorker")}>
              {row.leaseWorkerUid ?? t("labels.none")}
            </MetaRow>
            <MetaRow label={t("columns.leaseExpiry")}>
              <TimeAgoOrDash date={row.leaseExpiresAt} />
            </MetaRow>
            <MetaRow label={t("columns.attempts")}>{row.leaseStarts}</MetaRow>
            <MetaRow label={t("columns.updated")}>
              <TimeAgoOrDash date={row.updatedAt} data-testid="check-schedule-updated-at" />
            </MetaRow>
          </dl>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("detail.config")}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <JsonViewer value={row.config} emptyLabel={t("detail.noConfig")} />
          <div>
            <p className="text-sm text-muted-foreground">{t("detail.encryptedKeys")}</p>
            {row.encryptedKeys.length > 0 ? (
              <div className="mt-1 flex flex-wrap gap-1">
                {row.encryptedKeys.map((k) => (
                  <Badge key={k} variant="outline">
                    {k}
                  </Badge>
                ))}
              </div>
            ) : (
              <p className="mt-1 text-sm">{t("detail.noEncryptedKeys")}</p>
            )}
          </div>
          <p className="text-xs text-muted-foreground">{t("detail.secretsHidden")}</p>
        </CardContent>
      </Card>
    </div>
  );
}

function BackButton({ org, allOrgs }: { org: string; allOrgs: boolean }) {
  const { t } = useTranslation("jobs");
  const navigate = useNavigate();
  return (
    <Button
      variant="ghost"
      size="icon"
      aria-label={t("back")}
      onClick={() =>
        navigate({
          to: "/orgs/$org/jobs",
          params: { org },
          search: allOrgs ? { tab: "jobs", allOrgs: true } : { tab: "jobs" },
        })
      }
    >
      <ArrowLeft className="h-4 w-4" />
    </Button>
  );
}
