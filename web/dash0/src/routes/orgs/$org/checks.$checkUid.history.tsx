import { useState } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { AlertCircle, Check as CheckIcon, History, RotateCcw, X } from "lucide-react";
import { toast } from "sonner";
import {
  useCheck,
  useCheckVersionAction,
  useCheckVersionDiff,
  useCheckVersions,
  type CheckVersion,
} from "@/api/hooks";
import { PageHeader } from "@/components/shared/page-header";
import { Alert, AlertTitle } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { TimeAgo } from "@/components/ui/time-ago";
import { cn } from "@/lib/utils";

// Check version history (spec 2026-10-03-06). The selected version lives in
// the URL (?version=N) so a diff is deep-linkable.
interface HistorySearch {
  version?: number;
}

function validateHistorySearch(search: Record<string, unknown>): HistorySearch {
  const version = Number(search.version);

  return {
    version: Number.isInteger(version) && version > 0 ? version : undefined,
  };
}

export const Route = createFileRoute("/orgs/$org/checks/$checkUid/history")({
  validateSearch: validateHistorySearch,
  component: CheckHistoryPage,
});

function statusVariant(status: CheckVersion["status"]) {
  switch (status) {
    case "proposed":
      return "warning" as const;
    case "rejected":
      return "destructive" as const;
    default:
      return "secondary" as const;
  }
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

function CheckHistoryPage() {
  const { t } = useTranslation("checks");
  const { org, checkUid } = Route.useParams();
  const search = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });

  const { data: check, isLoading: checkLoading } = useCheck(org, checkUid);
  const { data: versionsData, isLoading } = useCheckVersions(org, checkUid);
  const versions = versionsData?.data ?? [];
  const proposals = versions.filter((v) => v.status === "proposed");
  const latestApplied = versions.find((v) => v.status === "applied");

  const selected =
    versions.find((v) => v.version === search.version) ?? latestApplied;

  const { data: diff, isLoading: diffLoading } = useCheckVersionDiff(
    org,
    checkUid,
    selected?.version,
  );

  const restore = useCheckVersionAction(org, checkUid, "restore");
  const approve = useCheckVersionAction(org, checkUid, "approve");
  const reject = useCheckVersionAction(org, checkUid, "reject");
  const [restoreTarget, setRestoreTarget] = useState<number | null>(null);

  const select = (version: number) =>
    navigate({ search: { version } });

  const run = (
    mutation: typeof restore,
    version: number,
    successKey: string,
  ) => {
    mutation.mutate(version, {
      onSuccess: () => {
        toast.success(t(successKey, { version }));
        navigate({ search: {} });
      },
      onError: (err) => toast.error(errorMessage(err)),
    });
  };

  if (!check && !checkLoading) {
    return (
      <div className="space-y-6">
        <PageHeader icon={History} title={t("history.title")} />
        <Alert variant="warning">
          <AlertCircle className="h-4 w-4" />
          <AlertTitle>{t("detail.notFound")}</AlertTitle>
        </Alert>
      </div>
    );
  }

  const originLabel = (v: CheckVersion) =>
    t(`history.origins.${v.origin}`, { defaultValue: v.origin });

  return (
    <div className="space-y-6">
      <PageHeader
        icon={History}
        title={t("history.title")}
        description={t("history.subtitle")}
        docsHref="/docs/features/check-history"
        className="flex-wrap"
      />

      {proposals.length > 0 && (
        <Card data-testid="check-history-proposals">
          <CardHeader>
            <CardTitle className="text-base">{t("history.proposals")}</CardTitle>
            <CardDescription>{t("history.proposalsDescription")}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            {proposals.map((v) => (
              <div
                key={v.version}
                className="flex flex-wrap items-center justify-between gap-3 rounded-md border p-3"
                data-testid={`check-proposal-${v.version}`}
              >
                <button
                  type="button"
                  className="min-w-0 text-left"
                  onClick={() => select(v.version)}
                >
                  <div className="font-medium">
                    {t("history.versionLabel", { version: v.version })} · {originLabel(v)}
                  </div>
                  {v.reason && (
                    <div className="truncate text-sm text-muted-foreground">{v.reason}</div>
                  )}
                </button>
                <div className="flex gap-2">
                  <Button
                    size="sm"
                    disabled={approve.isPending}
                    onClick={() => run(approve, v.version, "history.approved")}
                    data-testid={`check-proposal-approve-${v.version}`}
                  >
                    <CheckIcon className="mr-2 h-4 w-4" />
                    {t("history.approve")}
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={reject.isPending}
                    onClick={() => run(reject, v.version, "history.rejected")}
                    data-testid={`check-proposal-reject-${v.version}`}
                  >
                    <X className="mr-2 h-4 w-4" />
                    {t("history.reject")}
                  </Button>
                </div>
              </div>
            ))}
          </CardContent>
        </Card>
      )}

      <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)]">
        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("history.versions")}</CardTitle>
          </CardHeader>
          <CardContent className="p-0">
            {isLoading ? (
              <div className="space-y-2 p-4">
                <Skeleton className="h-8 w-full" />
                <Skeleton className="h-8 w-full" />
              </div>
            ) : versions.length === 0 ? (
              <p className="p-4 text-sm text-muted-foreground">{t("history.empty")}</p>
            ) : (
              <Table data-testid="check-history-list">
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("history.version")}</TableHead>
                    <TableHead>{t("history.change")}</TableHead>
                    <TableHead className="text-right">{t("history.when")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {versions.map((v) => (
                    <TableRow
                      key={v.version}
                      onClick={() => select(v.version)}
                      className={cn(
                        "cursor-pointer",
                        selected?.version === v.version && "bg-muted",
                      )}
                      data-testid={`check-version-row-${v.version}`}
                    >
                      <TableCell className="align-top">
                        <div className="font-medium">
                          {t("history.versionLabel", { version: v.version })}
                        </div>
                        {v.status !== "applied" && (
                          <Badge variant={statusVariant(v.status)} className="mt-1">
                            {t(`history.statuses.${v.status}`)}
                          </Badge>
                        )}
                      </TableCell>
                      <TableCell className="align-top">
                        <div className="text-sm">
                          {originLabel(v)}
                          {v.actorName && ` · ${v.actorName}`}
                        </div>
                        {v.reason && (
                          <div className="text-xs text-muted-foreground">{v.reason}</div>
                        )}
                      </TableCell>
                      <TableCell className="text-right align-top text-sm text-muted-foreground">
                        <TimeAgo date={v.createdAt} />
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>

        <Card data-testid="check-history-diff">
          <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-3 space-y-0">
            <div className="space-y-1">
              <CardTitle className="text-base">
                {selected
                  ? diff?.against
                    ? t("history.diffTitle", {
                        version: selected.version,
                        against: diff.against,
                      })
                    : t("history.versionLabel", { version: selected.version })
                  : t("history.diff")}
              </CardTitle>
              {selected && !diff?.against && !diffLoading && (
                <CardDescription>{t("history.firstVersion")}</CardDescription>
              )}
            </div>
            {selected &&
              selected.status === "applied" &&
              latestApplied &&
              selected.version !== latestApplied.version && (
                <Button
                  size="sm"
                  variant="outline"
                  disabled={restore.isPending}
                  onClick={() => setRestoreTarget(selected.version)}
                  data-testid="check-version-restore"
                >
                  <RotateCcw className="mr-2 h-4 w-4" />
                  {t("history.restore")}
                </Button>
              )}
          </CardHeader>
          <CardContent>
            {diffLoading ? (
              <Skeleton className="h-24 w-full" />
            ) : !diff || diff.changes.length === 0 ? (
              <p className="text-sm text-muted-foreground">{t("history.noChanges")}</p>
            ) : (
              <div className="overflow-x-auto">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t("history.field")}</TableHead>
                      <TableHead>{t("history.before")}</TableHead>
                      <TableHead>{t("history.after")}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {diff.changes.map((change) => (
                      <TableRow key={change.field} data-testid={`check-diff-${change.field}`}>
                        <TableCell className="font-mono text-xs">{change.field}</TableCell>
                        <TableCell className="break-all font-mono text-xs text-status-error-foreground">
                          {change.from}
                        </TableCell>
                        <TableCell className="break-all font-mono text-xs text-status-ok-foreground">
                          {change.to}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            )}
          </CardContent>
        </Card>
      </div>

      <AlertDialog
        open={restoreTarget !== null}
        onOpenChange={(open) => !open && setRestoreTarget(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("history.restoreTitle", { version: restoreTarget })}
            </AlertDialogTitle>
            <AlertDialogDescription>{t("history.restoreDescription")}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("history.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              data-testid="check-version-restore-confirm"
              onClick={() => {
                if (restoreTarget !== null) {
                  run(restore, restoreTarget, "history.restored");
                }
                setRestoreTarget(null);
              }}
            >
              {t("history.restore")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
