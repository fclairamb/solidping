import { useMemo, useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import {
  ArrowUpRight,
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

export const Route = createFileRoute("/orgs/$org/escalation-policies/")({
  component: EscalationPoliciesListPage,
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
  const [search, setSearch] = useState("");
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
    deleteMutation.mutate(pendingDelete.slug, {
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

      <div className="rounded-md border">
        {isLoading ? (
          <div className="space-y-2 p-2">
            {[...Array(6)].map((_, i) => (
              <Skeleton key={i} className="h-12 rounded-lg" />
            ))}
          </div>
        ) : isEmpty ? (
          <p className="py-12 text-center text-sm text-muted-foreground">
            {t("escalation:list.empty")}
          </p>
        ) : hasSearchButNoMatches ? (
          <p className="py-12 text-center text-sm text-muted-foreground">
            {t("escalation:list.noMatches")}
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("escalation:list.col.name")}</TableHead>
                <TableHead>{t("escalation:list.col.description")}</TableHead>
                <TableHead>{t("escalation:list.col.repeats")}</TableHead>
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
        )}
      </div>

      <AlertDialog
        open={!!pendingDelete}
        onOpenChange={(open) => (open ? null : setPendingDelete(null))}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("common:confirmDelete")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("escalation:editor.deleteConfirm")}
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

  return (
    <TableRow data-testid="policy-row">
      <TableCell>
        <Link
          to="/orgs/$org/escalation-policies/$slug"
          params={{ org, slug: policy.slug }}
          className="font-medium hover:underline"
        >
          {policy.name}
        </Link>
      </TableCell>
      <TableCell className="max-w-[420px] truncate text-sm text-muted-foreground">
        {policy.description || ""}
      </TableCell>
      <TableCell className="text-sm text-muted-foreground">
        {policy.repeatMax > 0 && policy.repeatAfterSeconds ? (
          <span className="inline-flex items-center gap-1">
            <Repeat className="h-3.5 w-3.5" />
            {t("escalation:list.repeats", {
              count: policy.repeatMax,
              seconds: policy.repeatAfterSeconds,
            })}
          </span>
        ) : (
          "—"
        )}
      </TableCell>
      <TableCell>
        <div className="flex items-center justify-end gap-1">
          <Button asChild variant="ghost" size="icon" aria-label={t("common:edit")}>
            <Link
              to="/orgs/$org/escalation-policies/$slug"
              params={{ org, slug: policy.slug }}
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
