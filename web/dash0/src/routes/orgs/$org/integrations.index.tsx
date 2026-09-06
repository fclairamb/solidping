import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import {
  Bell,
  Loader2,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  Star,
  Trash2,
  Wand2,
} from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { PageHeader } from "@/components/shared/page-header";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
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
import {
  useIntegrations,
  useCreateIntegration,
  useDeleteIntegration,
  type Integration,
  type ConnectionType,
} from "@/api/hooks";
import {
  IntegrationIcon,
  integrationLabel,
} from "@/components/integrations/integration-icon";
import { SMSModePanel } from "@/components/integrations/sms-mode-panel";
import { useDebounce } from "@/lib/use-debounce";
import { useAuth } from "@/contexts/AuthContext";
import { DemoReadOnlyNote } from "@/components/shared/demo-read-only-note";
import { useIsDemoSession } from "@/hooks/use-is-demo-session";
import { hasNotifiableIntegration } from "@/lib/onboarding-checklist";
import { buildEmailAlertsWandPayload } from "@/lib/onboarding-wand";

interface IntegrationsIndexSearch {
  q?: string;
}

export const Route = createFileRoute("/orgs/$org/integrations/")({
  component: IntegrationsListPage,
  validateSearch: (search: Record<string, unknown>): IntegrationsIndexSearch => ({
    q: typeof search.q === "string" && search.q ? search.q : undefined,
  }),
});

function IntegrationsListPage() {
  const { t } = useTranslation("integrations");
  const isDemoSession = useIsDemoSession();
  const { org } = Route.useParams();
  const {
    data: integrations,
    isLoading,
    isRefetching,
    refetch,
  } = useIntegrations(org);
  const deleteMutation = useDeleteIntegration(org);
  const createIntegration = useCreateIntegration(org);
  const { user } = useAuth();

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
  const [pendingDelete, setPendingDelete] = useState<Integration | null>(null);

  const onConfirmDelete = () => {
    if (!pendingDelete) return;
    deleteMutation.mutate(pendingDelete.uid, {
      onSuccess: () => {
        toast.success(t("deleted", "Integration deleted"));
        setPendingDelete(null);
      },
      onError: () => toast.error(t("deleteFailed", "Delete failed")),
    });
  };

  const filtered = useMemo(() => {
    const list = integrations || [];
    if (!search.trim()) return list;
    const q = search.toLowerCase();
    return list.filter(
      (c) =>
        c.name.toLowerCase().includes(q) || c.type.toLowerCase().includes(q),
    );
  }, [integrations, search]);

  const showEmptyState =
    !isLoading && integrations && integrations.length === 0;

  // Visible only once we know the org actually lacks a notifiable channel —
  // guarding on isLoading avoids a flash-then-disappear for an org that
  // already has one.
  const showWand = !isLoading && !hasNotifiableIntegration(integrations);

  const onWandClick = () => {
    if (!user?.email) return;
    createIntegration.mutate(buildEmailAlertsWandPayload(t, user.email), {
      onSuccess: () => toast.success(t("wand.created", "Email alerts created")),
      onError: () =>
        toast.error(t("wand.createFailed", "Failed to set up email alerts")),
    });
  };

  return (
    <div className="space-y-6">
      <PageHeader
        icon={Bell}
        title={t("title", "Integrations")}
        description={t(
          "subtitle",
          "Slack, webhooks, email, and more — wire how SolidPing reaches you.",
        )}
        docsHref="/docs/configuration/notifications"
        actions={
          <div className="flex flex-wrap items-center gap-2">
            {showWand && (
              <Button
                variant="outline"
                onClick={onWandClick}
                disabled={createIntegration.isPending}
                data-testid="wand-create-email-alerts"
                aria-label={t("wand.createEmailAlerts", "Set up email alerts for me")}
              >
                {createIntegration.isPending ? (
                  <Loader2 className="h-4 w-4 animate-spin sm:mr-2" />
                ) : (
                  <Wand2 className="h-4 w-4 sm:mr-2" />
                )}
                <span className="hidden sm:inline">
                  {t("wand.createEmailAlerts", "Set up email alerts for me")}
                </span>
              </Button>
            )}
            {isDemoSession ? (
              // A demo session cannot create an integration — the server's
              // write guard refuses every route but the four check ones — so
              // the button is replaced rather than disabled (spec
              // 2026-09-06-02).
              <DemoReadOnlyNote testId="integrations-demo-note" />
            ) : (
              <Button asChild aria-label={t("new", "New integration")}>
                <Link to="/orgs/$org/integrations/new" params={{ org }}>
                  <Plus className="h-4 w-4" />
                  <span className="hidden sm:inline">
                    {t("new", "New integration")}
                  </span>
                </Link>
              </Button>
            )}
          </div>
        }
        className="flex-wrap"
      />

      <AlertDialog
        open={!!pendingDelete}
        onOpenChange={(open) => (open ? null : setPendingDelete(null))}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("deleteConfirmTitle", "Delete integration?")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {pendingDelete
                ? t("deleteConfirmBody", "This will permanently delete \"{{name}}\".", {
                    name: pendingDelete.name,
                  })
                : ""}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("cancel", "Cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={onConfirmDelete}>
              {t("actions.delete", "Delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {showEmptyState ? (
        <EmptyState org={org} />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-4">
            <div className="relative flex-1 min-w-[200px] max-w-sm">
              <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
              <Input
                placeholder={t("searchPlaceholder", "Search by name or type…")}
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                className="pl-9"
              />
            </div>
            <Button
              variant="outline"
              onClick={() => refetch()}
              disabled={isRefetching}
              data-testid="integrations-refresh"
              aria-label={t("common:refresh")}
            >
              <RefreshCw
                className={`h-4 w-4 sm:mr-2 ${isRefetching ? "animate-spin" : ""}`}
              />
              <span className="hidden sm:inline">{t("common:refresh")}</span>
            </Button>
          </div>

          <div className="rounded-xl border bg-card shadow-card overflow-hidden">
            {isLoading ? (
              <div className="space-y-2 p-4">
                {[...Array(3)].map((_, i) => (
                  <Skeleton key={i} className="h-12 rounded-lg" />
                ))}
              </div>
            ) : (
              <Table>
                <TableHeader className="bg-muted/30">
                  <TableRow>
                    <TableHead>{t("col.name", "Name")}</TableHead>
                    <TableHead className="hidden sm:table-cell">
                      {t("col.type", "Type")}
                    </TableHead>
                    <TableHead>{t("col.status", "Status")}</TableHead>
                    <TableHead className="hidden md:table-cell">
                      {t("col.usedBy", "Used by")}
                    </TableHead>
                    <TableHead className="hidden lg:table-cell">
                      {t("col.updated", "Updated")}
                    </TableHead>
                    <TableHead className="w-[100px] text-right" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {filtered.map((c) => (
                    <Row
                      key={c.uid}
                      org={org}
                      integration={c}
                      onDelete={() => setPendingDelete(c)}
                    />
                  ))}
                </TableBody>
              </Table>
            )}
          </div>
        </>
      )}

      {/* Where this org's SMS actually comes from. Rendered last in BOTH
          states, collapsed by default: it's a contextual explainer an admin
          reads once, not primary page content — the collapsed header still
          carries the mode badge, so "SMS already works, the server provides
          it" stays visible apart from "SMS is not configured at all". */}
      <SMSModePanel org={org} integrations={integrations} isLoading={isLoading} />
    </div>
  );
}

function EmptyState({ org }: { org: string }) {
  const { t } = useTranslation("integrations");
  const quick: ConnectionType[] = ["slack", "discord", "email", "webhook"];

  return (
    <Card className="shadow-card">
      <CardHeader>
        <CardTitle className="text-base font-semibold">{t("empty.title", "No integrations yet")}</CardTitle>
        <CardDescription>
          {t(
            "empty.body",
            "Add an integration to start receiving notifications. Pick one to get going:",
          )}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-wrap gap-2.5">
        {quick.map((type) => (
          <Button key={type} variant="outline" size="sm" className="h-9 px-3.5 gap-2 font-medium" asChild>
            <Link
              to="/orgs/$org/integrations/new"
              params={{ org }}
              search={{ type }}
            >
              <IntegrationIcon type={type} className="h-4 w-4" />
              {integrationLabel(type)}
            </Link>
          </Button>
        ))}
      </CardContent>
    </Card>
  );
}

interface RowProps {
  org: string;
  integration: Integration;
  onDelete: () => void;
}

function Row({ org, integration, onDelete }: RowProps) {
  const { t } = useTranslation("integrations");

  return (
    <TableRow className="hover:bg-muted/40 transition-colors">
      <TableCell className="max-w-0">
        <Link
          to="/orgs/$org/integrations/$integrationUid"
          params={{ org, integrationUid: integration.uid }}
          className="flex min-w-0 items-center gap-2.5 font-medium text-foreground hover:text-primary hover:underline transition-colors"
        >
          <div className="flex h-7 w-7 shrink-0 items-center justify-center rounded-lg bg-muted/60">
            <IntegrationIcon type={integration.type} className="h-4 w-4" />
          </div>
          <span className="min-w-0 truncate">{integration.name}</span>
        </Link>
      </TableCell>
      <TableCell className="hidden sm:table-cell">
        <Badge variant="outline" className="font-mono text-xs font-normal">
          {integrationLabel(integration.type)}
        </Badge>
      </TableCell>
      <TableCell className="text-sm">
        <div className="flex items-center gap-2">
          <Badge
            variant={integration.enabled ? "default" : "secondary"}
            className={
              integration.enabled
                ? "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400 border border-emerald-500/20 font-medium"
                : "font-normal"
            }
          >
            {integration.enabled ? t("status.enabled", "Enabled") : t("status.disabled", "Disabled")}
          </Badge>
          {integration.isDefault && (
            <span title={t("default", "Default")} className="flex items-center">
              <Star className="h-3.5 w-3.5 fill-amber-400 text-amber-500" />
            </span>
          )}
        </div>
      </TableCell>
      <TableCell className="hidden text-xs text-muted-foreground font-mono md:table-cell">
        —
      </TableCell>
      <TableCell className="hidden text-xs text-muted-foreground font-mono lg:table-cell">
        {new Date(integration.updatedAt).toLocaleDateString()}
      </TableCell>
      <TableCell className="text-right">
        <div className="flex items-center justify-end gap-1">
          <Button asChild variant="ghost" size="icon" aria-label={t("actions.edit", "Edit")}>
            <Link
              to="/orgs/$org/integrations/$integrationUid"
              params={{ org, integrationUid: integration.uid }}
            >
              <Pencil className="h-4 w-4" />
            </Link>
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="text-destructive hover:text-destructive"
            onClick={onDelete}
            aria-label={t("actions.delete", "Delete")}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </TableCell>
    </TableRow>
  );
}
