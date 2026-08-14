import { useTranslation } from "react-i18next";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft, Workflow } from "lucide-react";

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
import {
  useBackgroundJob,
  useBackgroundJobChain,
  type BackgroundJob,
} from "@/api/hooks";

interface JobDetailSearch {
  allOrgs?: boolean;
}

export const Route = createFileRoute("/orgs/$org/jobs/$jobUid")({
  component: BackgroundJobDetailPage,
  validateSearch: (search: Record<string, unknown>): JobDetailSearch => ({
    allOrgs: search.allOrgs === true || search.allOrgs === "true",
  }),
});

type BadgeVariant = "default" | "secondary" | "destructive" | "outline" | "success" | "warning";

function jobStatusVariant(status: string): BadgeVariant {
  switch (status) {
    case "success":
      return "success";
    case "running":
      return "default";
    case "pending":
      return "secondary";
    case "retried":
      return "warning";
    case "failed":
      return "destructive";
    default:
      return "outline";
  }
}

function MetaRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5 sm:flex-row sm:gap-2">
      <dt className="w-40 shrink-0 text-sm text-muted-foreground">{label}</dt>
      <dd className="text-sm">{children}</dd>
    </div>
  );
}

function BackgroundJobDetailPage() {
  const { t } = useTranslation("jobs");
  const { org, jobUid } = Route.useParams();
  const { allOrgs } = Route.useSearch();

  const scope = { allOrgs: allOrgs ?? false };
  const { data: job, isLoading, isError } = useBackgroundJob(org, jobUid, scope);
  const { data: chain } = useBackgroundJobChain(org, jobUid, scope);

  if (isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-10 w-64" />
        <Skeleton className="h-40 w-full" />
      </div>
    );
  }

  if (isError || !job) {
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
          <Workflow className="h-5 w-5" />
        </div>
        <div className="min-w-0 flex-1">
          <h1 className="flex items-center gap-2 text-2xl font-semibold tracking-tight">
            <span className="truncate">{job.type}</span>
            <Badge variant={jobStatusVariant(job.status)}>
              {t(`status.${job.status}`, job.status)}
            </Badge>
          </h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {t("detail.backgroundJobTitle")}
          </p>
        </div>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t("detail.meta")}</CardTitle>
        </CardHeader>
        <CardContent>
          <dl className="space-y-2">
            <MetaRow label={t("columns.org")}>
              {job.organizationUid ?? <Badge variant="outline">{t("labels.system")}</Badge>}
            </MetaRow>
            <MetaRow label={t("columns.type")}>{job.type}</MetaRow>
            <MetaRow label={t("columns.status")}>
              <Badge variant={jobStatusVariant(job.status)}>
                {t(`status.${job.status}`, job.status)}
              </Badge>
            </MetaRow>
            <MetaRow label={t("columns.scheduled")}>
              <TimeAgoOrDash date={job.scheduledAt} data-testid="job-scheduled-at" />
            </MetaRow>
            <MetaRow label="Created">
              <TimeAgoOrDash date={job.createdAt} data-testid="job-created-at" />
            </MetaRow>
            <MetaRow label={t("columns.updated")}>
              <TimeAgoOrDash date={job.updatedAt} data-testid="job-updated-at" />
            </MetaRow>
            <MetaRow label={t("columns.retries")}>{job.retryCount}</MetaRow>
          </dl>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("detail.config")}</CardTitle>
        </CardHeader>
        <CardContent>
          <JsonViewer value={job.config} emptyLabel={t("detail.noConfig")} />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("detail.output")}</CardTitle>
        </CardHeader>
        <CardContent>
          <JsonViewer value={job.output} emptyLabel={t("detail.noOutput")} />
        </CardContent>
      </Card>

      {chain && chain.length > 1 && (
        <Card>
          <CardHeader>
            <CardTitle>{t("detail.retryChain")}</CardTitle>
          </CardHeader>
          <CardContent>
            <ol className="space-y-2">
              {chain.map((entry: BackgroundJob, idx) => {
                const isCurrent = entry.uid === job.uid;
                return (
                  <li key={entry.uid} className="flex items-center gap-2 text-sm">
                    <span className="w-6 text-right text-muted-foreground tabular-nums">
                      {idx + 1}.
                    </span>
                    <Badge variant={jobStatusVariant(entry.status)}>
                      {t(`status.${entry.status}`, entry.status)}
                    </Badge>
                    {isCurrent ? (
                      <span className="font-medium">{t("detail.thisJob")}</span>
                    ) : (
                      <Link
                        to="/orgs/$org/jobs/$jobUid"
                        params={{ org, jobUid: entry.uid }}
                        search={scope.allOrgs ? { allOrgs: true } : {}}
                        className="font-mono text-xs hover:underline"
                      >
                        {entry.uid}
                      </Link>
                    )}
                    <span className="text-xs text-muted-foreground">
                      <TimeAgoOrDash date={entry.updatedAt} />
                    </span>
                  </li>
                );
              })}
            </ol>
          </CardContent>
        </Card>
      )}
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
