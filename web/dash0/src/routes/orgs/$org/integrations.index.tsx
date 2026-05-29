import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link } from "@tanstack/react-router";
import { Bell, Pencil, Plus, RefreshCw, Search, Star, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
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
  useDeleteIntegration,
  type Integration,
  type ConnectionType,
} from "@/api/hooks";
import {
  IntegrationIcon,
  integrationLabel,
} from "@/components/integrations/integration-icon";

export const Route = createFileRoute("/orgs/$org/integrations/")({
  component: IntegrationsListPage,
});

function IntegrationsListPage() {
  const { t } = useTranslation("integrations");
  const { org } = Route.useParams();
  const {
    data: integrations,
    isLoading,
    isRefetching,
    refetch,
  } = useIntegrations(org);
  const deleteMutation = useDeleteIntegration(org);

  const [search, setSearch] = useState("");
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

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight flex items-center gap-2">
            <Bell className="h-7 w-7 text-muted-foreground" />
            {t("title", "Integrations")}
          </h1>
          <p className="text-muted-foreground">
            {t(
              "subtitle",
              "Slack, webhooks, email, and more — wire how SolidPing reaches you.",
            )}
          </p>
        </div>
        <Button asChild aria-label={t("new", "New integration")}>
          <Link to="/orgs/$org/integrations/new" params={{ org }}>
            <Plus className="h-4 w-4" />
            <span className="hidden sm:inline">
              {t("new", "New integration")}
            </span>
          </Link>
        </Button>
      </div>

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
              size="icon"
              onClick={() => refetch()}
              disabled={isRefetching}
              data-testid="integrations-refresh"
            >
              <RefreshCw
                className={`h-4 w-4 ${isRefetching ? "animate-spin" : ""}`}
              />
            </Button>
          </div>

          <div className="rounded-md border">
            {isLoading ? (
              <div className="space-y-2 p-2">
                {[...Array(6)].map((_, i) => (
                  <Skeleton key={i} className="h-12 rounded-lg" />
                ))}
              </div>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("col.name", "Name")}</TableHead>
                    <TableHead>{t("col.type", "Type")}</TableHead>
                    <TableHead>{t("col.status", "Status")}</TableHead>
                    <TableHead>{t("col.usedBy", "Used by")}</TableHead>
                    <TableHead>{t("col.updated", "Updated")}</TableHead>
                    <TableHead className="w-[100px]" />
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
    </div>
  );
}

function EmptyState({ org }: { org: string }) {
  const { t } = useTranslation("integrations");
  const quick: ConnectionType[] = ["slack", "discord", "email", "webhook"];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("empty.title", "No integrations yet")}</CardTitle>
        <CardDescription>
          {t(
            "empty.body",
            "Add an integration to start receiving notifications. Pick one to get going:",
          )}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-wrap gap-2">
        {quick.map((type) => (
          <Button key={type} variant="outline" size="sm" asChild>
            <Link
              to="/orgs/$org/integrations/new"
              params={{ org }}
              search={{ type }}
            >
              <IntegrationIcon type={type} className="h-4 w-4 mr-1" />
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
    <TableRow>
      <TableCell>
        <Link
          to="/orgs/$org/integrations/$integrationUid"
          params={{ org, integrationUid: integration.uid }}
          className="flex items-center gap-2 font-medium hover:underline"
        >
          <IntegrationIcon type={integration.type} className="h-4 w-4" />
          {integration.name}
        </Link>
      </TableCell>
      <TableCell>
        <Badge variant="outline">{integrationLabel(integration.type)}</Badge>
      </TableCell>
      <TableCell className="text-sm">
        <div className="flex items-center gap-2">
          <Badge variant={integration.enabled ? "default" : "secondary"}>
            {integration.enabled ? t("status.enabled", "Enabled") : t("status.disabled", "Disabled")}
          </Badge>
          {integration.isDefault && (
            <span title={t("default", "Default")}>
              <Star className="h-3 w-3 fill-yellow-500 stroke-yellow-600" />
            </span>
          )}
        </div>
      </TableCell>
      <TableCell className="text-sm text-muted-foreground">—</TableCell>
      <TableCell className="text-sm text-muted-foreground">
        {new Date(integration.updatedAt).toLocaleDateString()}
      </TableCell>
      <TableCell>
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
