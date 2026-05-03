import { useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import {
  Plus,
  Search,
  RefreshCw,
  MoreVertical,
  Trash2,
  Star,
  Globe,
} from "lucide-react";
import { toast } from "sonner";
import { useStatusPages, useDeleteStatusPage, type StatusPage } from "@/api/hooks";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
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
import { QueryErrorView } from "@/components/shared/error-views";
import { ApiError } from "@/api/client";

export const Route = createFileRoute("/orgs/$org/status-pages/")({
  component: StatusPagesIndexPage,
});

function StatusPageRow({
  page,
  org,
  onDelete,
}: {
  page: StatusPage;
  org: string;
  onDelete: (uid: string) => void;
}) {
  const { t } = useTranslation("statusPages");
  return (
    <TableRow>
      <TableCell>
        <Link
          to="/orgs/$org/status-pages/$statusPageUid"
          params={{ org, statusPageUid: page.uid }}
          className="flex items-center gap-2 hover:underline font-medium"
        >
          {page.name}
          {page.isDefault && <Star className="h-3 w-3 text-yellow-500 fill-yellow-500" />}
        </Link>
      </TableCell>
      <TableCell className="text-muted-foreground">{page.slug}</TableCell>
      <TableCell>
        <Badge variant={page.visibility === "public" ? "default" : "secondary"}>
          {page.visibility === "public"
            ? t("visibility.public")
            : t("visibility.restricted")}
        </Badge>
      </TableCell>
      <TableCell>
        <Badge variant={page.enabled ? "default" : "outline"}>
          {page.enabled ? t("enabled") : t("disabled")}
        </Badge>
      </TableCell>
      <TableCell>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon" className="h-8 w-8">
              <MoreVertical className="h-4 w-4" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem asChild>
              <Link
                to="/orgs/$org/status-pages/$statusPageUid"
                params={{ org, statusPageUid: page.uid }}
              >
                {t("viewDetails")}
              </Link>
            </DropdownMenuItem>
            <DropdownMenuItem asChild>
              <Link
                to="/orgs/$org/status-pages/$statusPageUid/edit"
                params={{ org, statusPageUid: page.uid }}
              >
                {t("edit")}
              </Link>
            </DropdownMenuItem>
            <DropdownMenuItem
              className="text-destructive"
              onClick={() => onDelete(page.uid)}
            >
              <Trash2 className="mr-2 h-4 w-4" />
              {t("delete")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </TableCell>
    </TableRow>
  );
}

function StatusPagesIndexPage() {
  const { t } = useTranslation(["statusPages", "common"]);
  const { org } = Route.useParams();
  const [search, setSearch] = useState("");
  const [deleteUid, setDeleteUid] = useState<string | null>(null);

  const {
    data: pages,
    isLoading,
    error,
    refetch,
    isRefetching,
  } = useStatusPages(org);

  const deleteStatusPage = useDeleteStatusPage(org);

  const filteredPages =
    pages?.filter((page) => {
      const searchLower = search.toLowerCase();
      return (
        page.name.toLowerCase().includes(searchLower) ||
        page.slug.toLowerCase().includes(searchLower)
      );
    }) ?? [];

  const handleDelete = async () => {
    if (!deleteUid) return;
    try {
      await deleteStatusPage.mutateAsync(deleteUid);
      toast.success(t("statusPages:toast.deleted"));
      setDeleteUid(null);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t("statusPages:toast.deleteFailed"));
    }
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight flex items-center gap-2">
            <Globe className="h-7 w-7 text-muted-foreground" />
            {t("statusPages:title")}
          </h1>
          <p className="text-muted-foreground">{t("statusPages:subtitle")}</p>
        </div>
        <Link to="/orgs/$org/status-pages/new" params={{ org }}>
          <Button>
            <Plus className="mr-2 h-4 w-4" />
            {t("statusPages:newStatusPage")}
          </Button>
        </Link>
      </div>

      <div className="flex items-center gap-4">
        <div className="relative flex-1 max-w-sm">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder={t("statusPages:searchPlaceholder")}
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
        >
          <RefreshCw className={`h-4 w-4 ${isRefetching ? "animate-spin" : ""}`} />
        </Button>
      </div>

      {error ? (
        <QueryErrorView error={error} org={org} onRetry={() => refetch()} />
      ) : isLoading ? (
        <div className="space-y-2">
          {[...Array(3)].map((_, i) => (
            <Skeleton key={i} className="h-12 rounded-lg" />
          ))}
        </div>
      ) : filteredPages.length > 0 ? (
        <div className="rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("statusPages:table.name")}</TableHead>
                <TableHead>{t("statusPages:table.slug")}</TableHead>
                <TableHead>{t("statusPages:table.visibility")}</TableHead>
                <TableHead>{t("statusPages:table.status")}</TableHead>
                <TableHead className="w-[50px]" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {filteredPages.map((page) => (
                <StatusPageRow
                  key={page.uid}
                  page={page}
                  org={org}
                  onDelete={setDeleteUid}
                />
              ))}
            </TableBody>
          </Table>
        </div>
      ) : pages && pages.length > 0 ? (
        <div className="text-center py-12 text-muted-foreground">
          <Search className="h-8 w-8 mx-auto mb-2 opacity-50" />
          <p>{t("statusPages:noMatch")}</p>
        </div>
      ) : (
        <div className="text-center py-12 text-muted-foreground">
          <p className="mb-2">{t("statusPages:noStatusPages")}</p>
          <Link to="/orgs/$org/status-pages/new" params={{ org }}>
            <Button>
              <Plus className="mr-2 h-4 w-4" />
              {t("statusPages:createFirst")}
            </Button>
          </Link>
        </div>
      )}

      <AlertDialog open={!!deleteUid} onOpenChange={() => setDeleteUid(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("statusPages:deleteDialog.title")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("statusPages:deleteDialog.description")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common:cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDelete}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {t("statusPages:delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
