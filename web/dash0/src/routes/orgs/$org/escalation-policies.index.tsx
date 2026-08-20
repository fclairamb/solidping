import { useEffect, useMemo, useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import {
  ArrowUpRight,
  Cpu,
  Layers,
  Pencil,
  Plus,
  RefreshCw,
  Repeat,
  Search,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";

import {
  useDeleteEscalationPolicy,
  useEscalationPolicies,
  type EscalationPolicy,
} from "@/api/hooks";
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
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { QueryErrorView } from "@/components/shared/error-views";
import { PageHeader } from "@/components/shared/page-header";
import { formatDuration } from "@/lib/period-estimate";
import { useDebounce } from "@/lib/use-debounce";

interface EscalationPoliciesIndexSearch {
  q?: string;
}

export const Route = createFileRoute("/orgs/$org/escalation-policies/")({
  component: EscalationPoliciesListPage,
  validateSearch: (search: Record<string, unknown>): EscalationPoliciesIndexSearch => ({
    q: typeof search.q === "string" && search.q ? search.q : undefined,
  }),
});

function EscalationPoliciesListPage() {
  const { t } = useTranslation(["escalation", "common"]);
  const { org } = Route.useParams();
  const {
    data: policies,
    isLoading,
    isRefetching,
    error,
    refetch,
  } = useEscalationPolicies(org);
  const deleteMutation = useDeleteEscalationPolicy(org);
  const { q: qParam } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  // Seeded from the URL once on mount via a lazy initializer — the layout
  // route otherwise races a second search-validation pass that can drop the
  // param on a cold load (known dash0 pitfall, see checks.index.tsx). The
  // debounced value is written back below so the URL stays in sync.
  const [search, setSearch] = useState(() => qParam ?? "");
  const debouncedSearch = useDebounce(search, 300);
  useEffect(() => {
    const next = debouncedSearch || undefined;
    // Skip the no-op write-back that would otherwise fire on every mount.
    // `navigate` is anchored to this route (`from`), so a redundant call
    // re-targets it from wherever the router has since moved — on a
    // logged-out deep link the guard has already bounced to /login, and
    // navigating back re-enters the guard with login's own search params
    // (session_expired/returnTo) folded in. That nests returnTo one level
    // deeper on every pass: an unbounded redirect loop that grows the URL
    // until the renderer hangs. Only write when the URL is actually stale.
    if (next === qParam) return;
    void navigate({
      search: (prev) => ({ ...prev, q: next }),
      replace: true,
    });
  }, [debouncedSearch, qParam, navigate]);
  const [pendingDelete, setPendingDelete] = useState<EscalationPolicy | null>(
    null,
  );

  const filtered = useMemo(() => {
    const list = policies || [];
    const q = search.trim().toLowerCase();
    if (!q) return list;
    return list.filter((p) => {
      const haystack = [p.name, p.description].filter(Boolean).join(" ").toLowerCase();
      return haystack.includes(q);
    });
  }, [policies, search]);

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource={t("escalation:list.title")}
        onRetry={() => refetch()}
      />
    );
  }

  const onConfirmDelete = () => {
    if (!pendingDelete) return;
    deleteMutation.mutate(pendingDelete.uid, {
      onSuccess: () => {
        toast.success(t("common:delete"));
        setPendingDelete(null);
      },
      onError: () => toast.error(t("common:somethingWentWrong")),
    });
  };

  const isEmpty = !isLoading && (!policies || policies.length === 0);
  const hasSearchButNoMatches = !isEmpty && filtered.length === 0;

  return (
    <div className="space-y-6">
      <PageHeader
        icon={ArrowUpRight}
        title={t("escalation:list.title")}
        description={t("escalation:list.subtitle")}
        docsHref="/docs/features/on-call"
        actions={
          <Button asChild>
            <Link to="/orgs/$org/escalation-policies/new" params={{ org }}>
              <Plus className="h-4 w-4 mr-1" />
              {t("escalation:list.create")}
            </Link>
          </Button>
        }
        className="flex-wrap"
      />

      <div className="flex flex-wrap items-center gap-4">
        <div className="relative flex-1 min-w-[200px] max-w-sm">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder={t("escalation:list.searchPlaceholder")}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="pl-9"
            data-testid="policy-search"
          />
        </div>
        <Button
          variant="outline"
          onClick={() => refetch()}
          disabled={isRefetching}
          data-testid="policy-refresh"
          aria-label={t("common:refresh")}
        >
          <RefreshCw className={`h-4 w-4 sm:mr-2 ${isRefetching ? "animate-spin" : ""}`} />
          <span className="hidden sm:inline">{t("common:refresh")}</span>
        </Button>
      </div>

      {isLoading ? (
        <div className="space-y-2">
          {[...Array(6)].map((_, i) => (
            <Skeleton key={i} className="h-14 rounded-lg" />
          ))}
        </div>
      ) : isEmpty ? (
        <div className="space-y-3 rounded-xl border bg-card p-12 text-center shadow-card">
          <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted">
            <ArrowUpRight className="h-6 w-6 text-muted-foreground" />
          </div>
          <p className="text-sm font-medium text-foreground">
            {t("escalation:list.emptyTitle")}
          </p>
          <p className="mx-auto max-w-sm text-xs text-muted-foreground">
            {t("escalation:list.empty")}
          </p>
          <Button asChild size="sm">
            <Link to="/orgs/$org/escalation-policies/new" params={{ org }}>
              <Plus className="mr-1.5 h-3.5 w-3.5" />
              {t("escalation:list.create")}
            </Link>
          </Button>
        </div>
      ) : hasSearchButNoMatches ? (
        <div className="space-y-3 rounded-xl border bg-card p-12 text-center shadow-card">
          <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted">
            <Search className="h-6 w-6 text-muted-foreground" />
          </div>
          <p className="text-sm font-medium text-foreground">
            {t("escalation:list.noMatches")}
          </p>
        </div>
      ) : (
        <div className="overflow-hidden rounded-xl border bg-card shadow-card">
          {/* The card clips to its radius, so the table needs its own scroll
              container — without it the trailing columns (and the row actions)
              are unreachable on a narrow viewport rather than merely offscreen. */}
          <div className="overflow-x-auto">
          <Table>
            <TableHeader className="bg-muted/30">
              <TableRow>
                <TableHead>{t("escalation:list.col.name")}</TableHead>
                <TableHead className="w-[140px]">
                  {t("escalation:list.col.steps")}
                </TableHead>
                <TableHead className="w-[200px]">
                  {t("escalation:list.col.usedBy")}
                </TableHead>
                <TableHead className="w-[220px]">
                  {t("escalation:list.col.repeats")}
                </TableHead>
                <TableHead className="w-[100px]" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {filtered.map((policy) => (
                <PolicyRow
                  key={policy.uid}
                  org={org}
                  policy={policy}
                  onDelete={() => setPendingDelete(policy)}
                />
              ))}
            </TableBody>
          </Table>
          </div>
        </div>
      )}

      <AlertDialog
        open={!!pendingDelete}
        onOpenChange={(open) => (open ? null : setPendingDelete(null))}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("common:confirmDelete")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("escalation:editor.deleteConfirm")}
              {pendingDelete &&
                ((pendingDelete.usageCheckCount ?? 0) > 0 ||
                  (pendingDelete.usageGroupCount ?? 0) > 0) && (
                  <span
                    className="mt-2 block font-medium text-foreground"
                    data-testid="policy-delete-usage"
                  >
                    {t("escalation:list.deleteUsage", {
                      checks: pendingDelete.usageCheckCount ?? 0,
                      groups: pendingDelete.usageGroupCount ?? 0,
                      defaultValue:
                        "Used by {{checks}} checks and {{groups}} groups — they will fall back to inherited escalation.",
                    })}
                  </span>
                )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common:cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={onConfirmDelete}>
              {t("common:delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

interface PolicyRowProps {
  org: string;
  policy: EscalationPolicy;
  onDelete: () => void;
}

function PolicyRow({ org, policy, onDelete }: PolicyRowProps) {
  const { t } = useTranslation(["escalation", "common"]);

  const stepCount = policy.stepCount ?? policy.steps?.length ?? 0;
  const checkUsage = policy.usageCheckCount ?? 0;
  const groupUsage = policy.usageGroupCount ?? 0;
  const isUnused = checkUsage === 0 && groupUsage === 0;

  return (
    <TableRow
      data-testid="policy-row"
      className="transition-colors hover:bg-muted/40"
    >
      <TableCell>
        <div className="flex items-start gap-3">
          <span className="mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-lg bg-muted/60">
            <ArrowUpRight className="h-3.5 w-3.5 text-muted-foreground/70" />
          </span>
          <div className="min-w-0">
            <Link
              to="/orgs/$org/escalation-policies/$uid"
              params={{ org, uid: policy.uid }}
              className="font-medium text-foreground transition-colors hover:text-primary hover:underline"
            >
              {policy.name}
            </Link>
            {policy.description && (
              <p className="max-w-[420px] truncate text-xs text-muted-foreground">
                {policy.description}
              </p>
            )}
          </div>
        </div>
      </TableCell>
      <TableCell>
        {stepCount === 0 ? (
          // A zero-step policy is attached to checks but pages nobody — the
          // most dangerous state on this page, so it gets the warning tint
          // rather than a neutral count.
          <Badge
            variant="outline"
            className="border-amber-500/20 bg-amber-500/10 text-xs font-medium text-amber-700 dark:text-amber-400"
            data-testid="policy-silent-badge"
            title="Zero-step policy — pages nobody"
          >
            {t("escalation:list.silentBadge", "0 steps — silent")}
          </Badge>
        ) : (
          <span className="inline-flex items-center gap-1.5">
            <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-primary/10 text-[10px] font-bold text-primary">
              {stepCount}
            </span>
            <span className="text-xs text-muted-foreground">
              {t("escalation:list.stepsLabel", { count: stepCount })}
            </span>
          </span>
        )}
      </TableCell>
      <TableCell>
        {isUnused ? (
          <span className="text-xs text-muted-foreground/70">
            {t("escalation:list.unused")}
          </span>
        ) : (
          <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted-foreground">
            {checkUsage > 0 && (
              <span className="inline-flex items-center gap-1">
                <Cpu className="h-3 w-3 text-muted-foreground/70" />
                {t("escalation:list.usedChecks", { count: checkUsage })}
              </span>
            )}
            {groupUsage > 0 && (
              <span className="inline-flex items-center gap-1">
                <Layers className="h-3 w-3 text-muted-foreground/70" />
                {t("escalation:list.usedGroups", { count: groupUsage })}
              </span>
            )}
          </span>
        )}
      </TableCell>
      <TableCell>
        {policy.repeatMax > 0 && policy.repeatAfterSeconds ? (
          <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
            <Repeat className="h-3.5 w-3.5 shrink-0 text-muted-foreground/70" />
            {t("escalation:list.repeats", {
              count: policy.repeatMax,
              duration: formatDuration(policy.repeatAfterSeconds),
            })}
          </span>
        ) : (
          <span className="text-xs text-muted-foreground/70">—</span>
        )}
      </TableCell>
      <TableCell>
        <div className="flex items-center justify-end gap-1">
          <Button asChild variant="ghost" size="icon" aria-label={t("common:edit")}>
            <Link
              to="/orgs/$org/escalation-policies/$uid"
              params={{ org, uid: policy.uid }}
            >
              <Pencil className="h-4 w-4" />
            </Link>
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="text-destructive hover:text-destructive"
            onClick={onDelete}
            aria-label={t("common:delete")}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </TableCell>
    </TableRow>
  );
}
