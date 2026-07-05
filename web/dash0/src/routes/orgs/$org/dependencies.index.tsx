import { useMemo, useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { GitBranch, RefreshCw, Search } from "lucide-react";

import { useDependencyGraph, type DependencyKind } from "@/api/hooks";
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

export const Route = createFileRoute("/orgs/$org/dependencies/")({
  component: DependenciesIndexPage,
});

function DependenciesIndexPage() {
  const { t } = useTranslation(["dependencies", "common"]);
  const { org } = Route.useParams();
  const { data: graph, isLoading, isRefetching, error, refetch } =
    useDependencyGraph(org);
  const [filter, setFilter] = useState("");

  const nameByUid = useMemo(() => {
    const m = new Map<string, { name: string; slug: string; uid: string }>();
    for (const n of graph?.nodes ?? []) {
      m.set(n.uid, { name: n.name, slug: n.slug, uid: n.uid });
    }
    return m;
  }, [graph]);

  const rows = useMemo(() => {
    if (!graph) return [];
    const lower = filter.trim().toLowerCase();
    const matches = graph.edges.map((e) => ({
      uid: e.uid,
      kind: e.kind as DependencyKind,
      parent: nameByUid.get(e.parentCheckUid),
      child: nameByUid.get(e.childCheckUid),
    }));
    if (!lower) return matches;
    return matches.filter((r) => {
      const haystack = [
        r.parent?.name,
        r.parent?.slug,
        r.child?.name,
        r.child?.slug,
      ]
        .filter(Boolean)
        .join(" ")
        .toLowerCase();
      return haystack.includes(lower);
    });
  }, [graph, filter, nameByUid]);

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource={t("dependencies:list.title")}
        onRetry={() => refetch()}
      />
    );
  }

  return (
    <div className="space-y-6">
      <PageHeader
        icon={GitBranch}
        title={t("dependencies:list.title")}
        description={t("dependencies:list.subtitle")}
        className="flex-wrap"
      />

      <div className="flex flex-wrap items-center gap-4">
        <div className="relative flex-1 min-w-[200px] max-w-sm">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
          <Input
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder={t("dependencies:list.filter")}
            className="pl-9"
            data-testid="dependencies-filter"
          />
        </div>
        <Button
          variant="outline"
          onClick={() => refetch()}
          disabled={isRefetching}
          data-testid="dependencies-refresh"
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
        ) : rows.length === 0 ? (
          <p className="py-12 text-center text-sm text-muted-foreground">
            {t("dependencies:list.empty")}
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("dependencies:list.parent")}</TableHead>
                <TableHead className="w-12" />
                <TableHead>{t("dependencies:list.child")}</TableHead>
                <TableHead>{t("dependencies:list.kind")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((r) => (
                <TableRow key={r.uid} data-testid="dependency-row">
                  <TableCell>
                    {r.parent ? (
                      <Link
                        to="/orgs/$org/checks/$checkUid"
                        params={{ org, checkUid: r.parent.uid }}
                        search={{ graphPeriod: undefined, graphFull: undefined, graphRegion: undefined }}
                        className="hover:underline"
                      >
                        {r.parent.name || r.parent.slug}
                      </Link>
                    ) : (
                      "-"
                    )}
                  </TableCell>
                  <TableCell className="text-muted-foreground">→</TableCell>
                  <TableCell>
                    {r.child ? (
                      <Link
                        to="/orgs/$org/checks/$checkUid"
                        params={{ org, checkUid: r.child.uid }}
                        search={{ graphPeriod: undefined, graphFull: undefined, graphRegion: undefined }}
                        className="hover:underline"
                      >
                        {r.child.name || r.child.slug}
                      </Link>
                    ) : (
                      "-"
                    )}
                  </TableCell>
                  <TableCell>
                    <Badge
                      variant="outline"
                      className={
                        r.kind === "hard"
                          ? "bg-red-500/10 text-red-500"
                          : "bg-blue-500/10 text-blue-500"
                      }
                    >
                      {r.kind === "hard"
                        ? t("dependencies:kindHard")
                        : t("dependencies:kindSoft")}
                    </Badge>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>
    </div>
  );
}
