import { useEffect, useMemo, useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { ArrowLeft, ArrowRight, CalendarClock, Inbox, Wand2 } from "lucide-react";

import {
  useApplyCheckSchedule,
  useCheckTypes,
  useEntitlements,
  useInfiniteChecks,
  type Check,
  type CheckScheduleChange,
  type CheckTypeInfo,
} from "@/api/hooks";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { QueryErrorView } from "@/components/shared/error-views";
import { PageHeader } from "@/components/shared/page-header";
import { CheckTypeBadge } from "@/components/shared/check-type-identity";
import { CheckRateLimitBanner } from "@/components/shared/check-rate-limit-banner";
import { CheckRateMeter } from "@/components/shared/check-rate-meter";
import {
  anchoredDemand,
  buildSchedulingRows,
  describePeriod,
  formatRate,
  isRowDirty,
  periodOptionsFor,
  periodToHMS,
  proposeRebalance,
  rowContribution,
  savedTotalDemand,
  totalDemand,
  type SchedulingRow,
} from "@/lib/check-scheduling";

export const Route = createFileRoute("/orgs/$org/checks/scheduling")({
  component: CheckSchedulingPage,
});

/**
 * Renders a period the way the check form does, but through i18n rather than
 * hard-coded English: `describePeriod` picks the largest whole unit and the
 * plural-aware key does the rest.
 */
function usePeriodLabel() {
  const { t } = useTranslation(["checks"]);

  return (seconds: number) => {
    const { unit, count } = describePeriod(seconds);
    return t(`checks:scheduling.periodUnit.${unit}`, { count });
  };
}

interface SchedulingTableProps {
  org: string;
  rows: SchedulingRow[];
  proposals: Map<string, number>;
  onPeriodChange: (uid: string, seconds: number) => void;
  onEnabledChange: (uid: string, enabled: boolean) => void;
  disabled: boolean;
}

function SchedulingTable({
  org,
  rows,
  proposals,
  onPeriodChange,
  onEnabledChange,
  disabled,
}: SchedulingTableProps) {
  const { t } = useTranslation(["checks"]);
  const periodLabel = usePeriodLabel();

  return (
    <div className="overflow-hidden rounded-xl border bg-card shadow-card">
      <div className="overflow-x-auto">
        <Table>
          <TableHeader className="bg-muted/30">
            <TableRow>
              <TableHead>{t("checks:scheduling.table.check")}</TableHead>
              <TableHead className="hidden sm:table-cell">
                {t("checks:scheduling.table.type")}
              </TableHead>
              <TableHead>{t("checks:scheduling.table.period")}</TableHead>
              {/*
                Below `sm` the per-minute column folds into the name cell
                instead of sitting behind a horizontal scroll — the figure and
                the on/off switch are the point of the page, so a phone must
                not have to swipe sideways to reach them.
              */}
              <TableHead className="hidden text-right sm:table-cell">
                {t("checks:scheduling.table.contribution")}
              </TableHead>
              <TableHead className="text-right">
                {t("checks:scheduling.table.enabled")}
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row) => {
              const dirty = isRowDirty(row);
              const proposed = proposals.get(row.uid);
              const options = periodOptionsFor(row);

              return (
                <TableRow
                  key={row.uid}
                  className="transition-colors hover:bg-muted/40"
                  data-testid={`scheduling-row-${row.uid}`}
                  data-dirty={dirty ? "true" : "false"}
                >
                  <TableCell className="max-w-[8.5rem] px-2 sm:max-w-[14rem] sm:px-4">
                    <Link
                      to="/orgs/$org/checks/$checkUid"
                      params={{ org, checkUid: row.uid }}
                      search={{
                        graphPeriod: undefined,
                        graphFull: undefined,
                        region: undefined,
                      }}
                      className="block truncate font-medium text-foreground transition-colors hover:text-primary hover:underline"
                    >
                      {row.name}
                    </Link>
                    <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted-foreground">
                      <span className="sm:hidden">
                        <CheckTypeBadge type={row.type} />
                      </span>
                      {/*
                        Regions only when there is more than one: a single
                        region is the default and repeating it on every row
                        would bury the multi-region checks that are actually
                        multiplying the org's demand.
                      */}
                      {row.regions.length > 1 && (
                        <span data-testid={`scheduling-regions-${row.uid}`}>
                          {t("checks:scheduling.table.regionCount", {
                            count: row.regions.length,
                          })}
                        </span>
                      )}
                      <span className="font-mono tabular-nums sm:hidden">
                        {t("checks:scheduling.table.perMinuteShort", {
                          rate: formatRate(rowContribution(row)),
                        })}
                      </span>
                    </div>
                  </TableCell>
                  <TableCell className="hidden sm:table-cell">
                    <CheckTypeBadge type={row.type} />
                  </TableCell>
                  <TableCell className="px-2 sm:px-4">
                    <div className="flex flex-wrap items-center gap-2">
                      {/*
                        The per-row half of the auto-rebalance diff: the saved
                        period struck through next to what it would become. It
                        shows for ANY pending edit, not just proposed ones, so
                        a hand edit and a proposal read the same.
                      */}
                      {dirty &&
                        row.periodSeconds !== row.currentPeriodSeconds && (
                          <span
                            className="flex items-center gap-1 text-xs text-muted-foreground"
                            data-testid={`scheduling-diff-${row.uid}`}
                          >
                            <span className="line-through">
                              {periodLabel(row.currentPeriodSeconds)}
                            </span>
                            <ArrowRight className="h-3 w-3" aria-hidden />
                          </span>
                        )}
                      <Select
                        value={String(row.periodSeconds)}
                        onValueChange={(value) =>
                          onPeriodChange(row.uid, Number(value))
                        }
                        disabled={disabled}
                      >
                        <SelectTrigger
                          className="w-[7.5rem] sm:w-[9.5rem]"
                          data-testid={`scheduling-period-${row.uid}`}
                          aria-label={t("checks:scheduling.table.periodFor", {
                            name: row.name,
                          })}
                        >
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          {options.map((option) => (
                            <SelectItem
                              key={option.seconds}
                              value={String(option.seconds)}
                            >
                              {option.custom
                                ? t("checks:scheduling.customPeriod", {
                                    period: periodLabel(option.seconds),
                                  })
                                : periodLabel(option.seconds)}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                      {proposed !== undefined && (
                        <span className="text-xs text-status-warning-foreground">
                          {t("checks:scheduling.proposed")}
                        </span>
                      )}
                    </div>
                  </TableCell>
                  <TableCell
                    className="hidden text-right font-mono text-xs tabular-nums text-muted-foreground sm:table-cell"
                    data-testid={`scheduling-contribution-${row.uid}`}
                  >
                    {formatRate(rowContribution(row))}
                  </TableCell>
                  <TableCell className="px-2 text-right sm:px-4">
                    <Switch
                      checked={row.enabled}
                      onCheckedChange={(checked) =>
                        onEnabledChange(row.uid, checked)
                      }
                      disabled={disabled}
                      data-testid={`scheduling-enabled-${row.uid}`}
                      aria-label={t("checks:scheduling.table.enabledFor", {
                        name: row.name,
                      })}
                    />
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </div>
    </div>
  );
}

function CheckSchedulingPage() {
  const { t } = useTranslation(["checks", "common"]);
  const { org } = Route.useParams();

  const {
    data: pages,
    isLoading,
    error,
    refetch,
    hasNextPage,
    fetchNextPage,
    isFetchingNextPage,
  } = useInfiniteChecks(org, { limit: 100 });

  // This page's whole subject is the ORG total, so stopping at the list
  // endpoint's 100-row page would quietly under-report an org with more checks
  // than that — exactly the org most likely to be over its cap.
  useEffect(() => {
    if (hasNextPage && !isFetchingNextPage) {
      void fetchNextPage();
    }
  }, [hasNextPage, isFetchingNextPage, fetchNextPage]);

  const { data: entitlements } = useEntitlements(org);
  const { data: checkTypes } = useCheckTypes(org);
  const applySchedule = useApplyCheckSchedule(org);

  const checks: Check[] = useMemo(
    () => (pages?.pages ?? []).flatMap((page) => page.data ?? []),
    [pages],
  );

  const typeInfo = useMemo(() => {
    const map = new Map<string, CheckTypeInfo>();
    for (const info of checkTypes ?? []) {
      map.set(info.type, info);
    }
    return map;
  }, [checkTypes]);

  const { rows: serverRows, passiveCount } = useMemo(
    () => buildSchedulingRows(checks, typeInfo),
    [checks, typeInfo],
  );

  // Draft edits, keyed by uid. Deliberately NOT in the URL: a layout-route
  // search param is dropped on a cold deep link (a known dash0 pitfall), and a
  // half-applied schedule is not something anyone wants to bookmark or share.
  const [draft, setDraft] = useState<
    Record<string, { periodSeconds?: number; enabled?: boolean }>
  >({});
  const [proposals, setProposals] = useState<Map<string, number>>(new Map());
  const [rebalanceFailed, setRebalanceFailed] = useState(false);

  const rows = useMemo(
    () =>
      serverRows.map((row) => {
        const edit = draft[row.uid];
        if (!edit) return row;

        return {
          ...row,
          periodSeconds: edit.periodSeconds ?? row.periodSeconds,
          enabled: edit.enabled ?? row.enabled,
        };
      }),
    [serverRows, draft],
  );

  const limit = entitlements?.checksPerMinute?.limit;
  // The saved figure comes from the server (spec 2026-08-26-03), not from a
  // second client-side sum — it is what the over-limit banner on this page
  // quotes and what the rate gate enforces. Only the DELTA of the unsaved
  // edits is computed here, because only the client knows about them.
  const { saved: savedTotal, draft: draftTotal } = anchoredDemand(
    entitlements?.checksPerMinute?.demand,
    savedTotalDemand(rows),
    totalDemand(rows),
  );
  const dirtyRows = rows.filter(isRowDirty);
  const busy = applySchedule.isPending;
  const showRebalanceExhausted =
    rebalanceFailed &&
    limit !== null &&
    limit !== undefined &&
    draftTotal > limit;

  const setPeriod = (uid: string, seconds: number) => {
    setDraft((prev) => ({
      ...prev,
      [uid]: { ...prev[uid], periodSeconds: seconds },
    }));
    // A hand edit on a proposed row means the user has taken that row over.
    setProposals((prev) => {
      if (!prev.has(uid)) return prev;
      const next = new Map(prev);
      next.delete(uid);
      return next;
    });
  };

  const setEnabled = (uid: string, enabled: boolean) => {
    setDraft((prev) => ({ ...prev, [uid]: { ...prev[uid], enabled } }));
  };

  const resetDraft = () => {
    setDraft({});
    setProposals(new Map());
    setRebalanceFailed(false);
  };

  const runRebalance = () => {
    // Target the client-side budget matching the server's cap: the meter is
    // anchored on the server's figure, so if the two ever differ the proposal
    // must still land the number the USER is watching under the cap.
    const rebalanceLimit =
      limit === null || limit === undefined
        ? limit
        : limit - (savedTotal - savedTotalDemand(rows));

    const proposal = proposeRebalance(rows, rebalanceLimit);
    setRebalanceFailed(!proposal.reachedLimit);

    // Unconditional: a run that proposes nothing must also clear the markers a
    // previous run left behind, or rows keep claiming "proposed" for a
    // proposal that no longer exists.
    setProposals(proposal.proposals);
    setDraft((prev) => {
      const next = { ...prev };
      for (const [uid, seconds] of proposal.proposals) {
        next[uid] = { ...next[uid], periodSeconds: seconds };
      }
      return next;
    });
  };

  const apply = () => {
    const changes: CheckScheduleChange[] = dirtyRows.map((row) => ({
      uid: row.uid,
      name: row.name,
      ...(row.periodSeconds !== row.currentPeriodSeconds
        ? { period: periodToHMS(row.periodSeconds) }
        : {}),
      ...(row.enabled !== row.currentEnabled ? { enabled: row.enabled } : {}),
    }));

    applySchedule.mutate(changes, {
      onSuccess: (result) => {
        if (result.failures.length > 0) {
          toast.error(
            t("checks:scheduling.toast.partial", {
              applied: result.applied,
              failed: result.failures.length,
              names: result.failures.map((f) => f.name).join(", "),
            }),
          );
        } else {
          toast.success(
            t("checks:scheduling.toast.applied", { count: result.applied }),
          );
        }
        resetDraft();
      },
      onError: () => {
        toast.error(t("checks:scheduling.toast.failed"));
      },
    });
  };

  const header = (
    <PageHeader
      icon={CalendarClock}
      title={t("checks:scheduling.title")}
      description={t("checks:scheduling.subtitle")}
      actions={
        <Button asChild variant="outline">
          <Link to="/orgs/$org/checks" params={{ org }}>
            <ArrowLeft className="h-4 w-4 sm:mr-2" />
            <span className="hidden sm:inline">
              {t("checks:scheduling.backToChecks")}
            </span>
          </Link>
        </Button>
      }
      className="flex-wrap"
    />
  );

  if (error) {
    return (
      <div className="space-y-6">
        {header}
        <QueryErrorView
          error={error as Error}
          org={org}
          onRetry={() => void refetch()}
        />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {header}

      <CheckRateLimitBanner
        org={org}
        checksPerMinute={entitlements?.checksPerMinute}
        upgradeUrl={entitlements?.upgradeUrl}
      />

      <div className="rounded-xl border bg-card p-4 shadow-card">
        <CheckRateMeter saved={savedTotal} draft={draftTotal} limit={limit} />
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <Button
          variant="outline"
          onClick={runRebalance}
          disabled={busy || isLoading || rows.length === 0}
          data-testid="scheduling-rebalance-button"
          aria-label={t("checks:scheduling.autoRebalance")}
        >
          <Wand2 className="h-4 w-4 sm:mr-2" />
          <span className="hidden sm:inline">
            {t("checks:scheduling.autoRebalance")}
          </span>
        </Button>
        <div className="ml-auto flex flex-wrap items-center gap-2">
          {dirtyRows.length > 0 && (
            <span
              className="text-sm text-muted-foreground"
              data-testid="scheduling-pending-count"
            >
              {t("checks:scheduling.pending", { count: dirtyRows.length })}
            </span>
          )}
          <Button
            variant="ghost"
            onClick={resetDraft}
            disabled={busy || dirtyRows.length === 0}
            data-testid="scheduling-reset-button"
          >
            {t("checks:scheduling.reset")}
          </Button>
          <Button
            onClick={apply}
            disabled={busy || dirtyRows.length === 0}
            data-testid="scheduling-apply-button"
          >
            {t("checks:scheduling.apply")}
          </Button>
        </div>
      </div>

      {/*
        Gated on the LIVE total, not only on the flag: "stretching cannot get
        you there" stops being true the moment the user disables a check and
        the draft drops under the cap, and an alert that keeps insisting
        otherwise trains people to ignore it.
      */}
      {showRebalanceExhausted && (
        <Alert variant="warning" data-testid="scheduling-rebalance-exhausted">
          <AlertDescription>
            {t("checks:scheduling.rebalanceExhausted")}
          </AlertDescription>
        </Alert>
      )}

      {limit === null || limit === undefined ? null : proposals.size > 0 ? (
        <p
          className="text-sm text-muted-foreground"
          data-testid="scheduling-proposal-summary"
        >
          {t("checks:scheduling.proposalSummary", {
            count: proposals.size,
            before: formatRate(savedTotal),
            after: formatRate(draftTotal),
            limit: formatRate(limit),
          })}
        </p>
      ) : null}

      {isLoading ? (
        <div className="space-y-2">
          {[0, 1, 2, 3, 4].map((i) => (
            <Skeleton key={i} className="h-12 w-full" />
          ))}
        </div>
      ) : rows.length === 0 ? (
        <div className="space-y-3 rounded-xl border bg-card p-12 text-center shadow-card">
          <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted">
            <Inbox className="h-6 w-6 text-muted-foreground" />
          </div>
          <p className="text-sm font-medium text-foreground">
            {t("checks:scheduling.empty.title")}
          </p>
          <p className="mx-auto max-w-sm text-xs text-muted-foreground">
            {t("checks:scheduling.empty.hint")}
          </p>
        </div>
      ) : (
        <SchedulingTable
          org={org}
          rows={rows}
          proposals={proposals}
          onPeriodChange={setPeriod}
          onEnabledChange={setEnabled}
          disabled={busy}
        />
      )}

      {/*
        Passive checks are absent from the table on purpose — they return
        before the rate gate and cost nothing — so say so, or the user hunts
        for the heartbeat that "disappeared".
      */}
      {passiveCount > 0 && (
        <p
          className="text-xs text-muted-foreground"
          data-testid="scheduling-passive-note"
        >
          {t("checks:scheduling.passiveNote", { count: passiveCount })}
        </p>
      )}
    </div>
  );
}
