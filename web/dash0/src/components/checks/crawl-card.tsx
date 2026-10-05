import { useTranslation } from "react-i18next";
import { Download, Loader2, Network, Square } from "lucide-react";
import { toast } from "sonner";

import { useCancelCheckRun, useCheckRun, useCrawlReports } from "@/api/hooks";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import { TimeAgo } from "@/components/ui/time-ago";
import { crawlProgress, summarizeCrawlOutput } from "@/lib/crawl-findings";

export interface CrawlCardProps {
  org: string;
  /** The check's route identifier (uid or slug — the API takes both). */
  checkUid: string;
  /** The output of the check's last result (the last finished run). */
  output?: Record<string, unknown>;
  /** Whether the viewer may cancel a run (write access). */
  canCancel: boolean;
}

/**
 * CrawlCard is the website crawl section of the check page (spec
 * 2026-10-03-03): a progress line while a run is in progress (pages done out
 * of the page budget, when it started, Cancel), the last run's findings
 * grouped by type with the new ones flagged, and download links to the last
 * five reports.
 */
export function CrawlCard({ org, checkUid, output, canCancel }: CrawlCardProps) {
  const { t } = useTranslation("checks");
  const run = useCheckRun(org, checkUid);
  const reports = useCrawlReports(org, checkUid);
  const cancel = useCancelCheckRun(org, checkUid);
  const summary = summarizeCrawlOutput(output);
  const progress = crawlProgress(run.data?.progress);
  const hasRun = output !== undefined && Array.isArray(output.findings);

  const onCancel = () =>
    cancel.mutate(undefined, {
      onSuccess: () => toast.success(t("crawl.detail.cancelled")),
      onError: () => toast.error(t("crawl.detail.cancelFailed")),
    });

  return (
    <Card data-testid="crawl-card">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Network className="h-4 w-4" />
          {t("crawl.detail.title")}
        </CardTitle>
        {hasRun && (
          <CardDescription>
            {t("crawl.detail.pagesCrawled", { count: summary.pagesCrawled })}
            {summary.incomplete && ` · ${t("crawl.detail.incomplete")}`}
          </CardDescription>
        )}
      </CardHeader>
      <CardContent className="space-y-6">
        {run.data?.running && (
          <div className="space-y-2 rounded-md border p-3" data-testid="crawl-run-progress">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="flex items-center gap-2 text-sm">
                <Loader2 className="h-4 w-4 animate-spin" />
                <span>
                  {progress
                    ? t("crawl.detail.running", { done: progress.done, max: progress.max })
                    : t("crawl.detail.runningNoProgress")}
                </span>
                {run.data.startedAt && (
                  <span className="text-muted-foreground">
                    · {t("crawl.detail.startedAt")} <TimeAgo date={run.data.startedAt} />
                  </span>
                )}
              </div>
              {canCancel && (
                <Button
                  variant="outline"
                  size="sm"
                  className="min-h-9"
                  onClick={onCancel}
                  disabled={cancel.isPending}
                  data-testid="crawl-cancel-button"
                >
                  <Square className="mr-1 h-3 w-3" />
                  {t("crawl.detail.cancel")}
                </Button>
              )}
            </div>
            {progress && <Progress value={progress.done} max={progress.max} destructiveWhenFull={false} />}
          </div>
        )}

        {!hasRun ? (
          <p className="text-sm text-muted-foreground">{t("crawl.detail.noRun")}</p>
        ) : summary.groups.length === 0 ? (
          <p className="text-sm text-muted-foreground" data-testid="crawl-no-findings">
            {t("crawl.detail.noFindings")}
          </p>
        ) : (
          <div className="space-y-4" data-testid="crawl-findings">
            {summary.groups.map((group) => (
              <div key={group.type} className="space-y-2">
                <h4 className="flex items-center gap-2 text-sm font-medium">
                  {t(`crawl.finding.${group.type}`, group.type)}
                  <Badge variant="secondary">{group.count}</Badge>
                </h4>
                <ul className="space-y-1">
                  {group.findings.map((finding) => (
                    <li
                      key={finding.fingerprint}
                      className="flex flex-wrap items-baseline gap-x-2 break-all text-sm"
                    >
                      {finding.isNew && (
                        <Badge variant="destructive" className="shrink-0">
                          {t("crawl.detail.newBadge")}
                        </Badge>
                      )}
                      <a
                        href={finding.url}
                        target="_blank"
                        rel="noreferrer noopener"
                        className="font-mono text-xs underline-offset-2 hover:underline"
                      >
                        {finding.url}
                      </a>
                      {(finding.status || finding.error) && (
                        <span className="text-xs text-muted-foreground">
                          {finding.status ? finding.status : finding.error}
                        </span>
                      )}
                      {finding.source && (
                        <span className="text-xs text-muted-foreground">
                          {t("crawl.detail.on", { source: finding.source })}
                        </span>
                      )}
                    </li>
                  ))}
                </ul>
              </div>
            ))}
            {summary.total > summary.shown && (
              <p className="text-xs text-muted-foreground">
                {t("crawl.detail.showingFirst", { shown: summary.shown, total: summary.total })}
              </p>
            )}
          </div>
        )}

        <div className="space-y-2">
          <h4 className="text-sm font-medium">{t("crawl.detail.reports")}</h4>
          {(reports.data ?? []).length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("crawl.detail.noReports")}</p>
          ) : (
            <ul className="space-y-1" data-testid="crawl-reports">
              {(reports.data ?? []).map((report) => (
                <li key={report.uid}>
                  <a
                    href={report.downloadUrl}
                    download
                    className="inline-flex min-h-9 items-center gap-2 text-sm underline-offset-2 hover:underline"
                  >
                    <Download className="h-4 w-4" />
                    {t("crawl.detail.download")}
                    <span className="text-muted-foreground">
                      <TimeAgo date={report.capturedAt ?? report.createdAt} />
                    </span>
                  </a>
                </li>
              ))}
            </ul>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
