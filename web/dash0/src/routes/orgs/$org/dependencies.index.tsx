import { useEffect, useMemo, useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
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
import { useDebounce } from "@/lib/use-debounce";

interface DependenciesIndexSearch {
  q?: string;
}

export const Route = createFileRoute("/orgs/$org/dependencies/")({
  component: DependenciesIndexPage,
  validateSearch: (search: Record<string, unknown>): DependenciesIndexSearch => ({
    q: typeof search.q === "string" && search.q ? search.q : undefined,
  }),
});

function DependenciesIndexPage() {
  const { t } = useTranslation(["dependencies", "common"]);
  const { org } = Route.useParams();
  const { data: graph, isLoading, isRefetching, error, refetch } =
    useDependencyGraph(org);
  const { q: qParam } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  // Seeded from the URL once on mount via a lazy initializer — the layout
  // route otherwise races a second search-validation pass that can drop the
  // param on a cold load (known dash0 pitfall, see checks.index.tsx). The
  // debounced value is written back below so the URL stays in sync. Renamed
  // from `filter` to `search`/`setSearch` to match the common pattern used
  // across the other list routes (spec 2026-08-04-02).
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

  const nameByUid = useMemo(() => {
    const m = new Map<string, { name: string; slug: string; uid: string }>();
    for (const n of graph?.nodes ?? []) {
      m.set(n.uid, { name: n.name, slug: n.slug, uid: n.uid });
    }
    return m;
  }, [graph]);

  const rows = useMemo(() => {
    if (!graph) return [];
    const lower = search.trim().toLowerCase();
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
  }, [graph, search, nameByUid]);

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
        docsHref="/docs/features/incidents#group-incidents-correlated-outages"
        className="flex-wrap"
      />

      <div className="flex flex-wrap items-center gap-4">
        <div className="relative flex-1 min-w-[200px] max-w-sm">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
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

      {isLoading ? (
        <div className="space-y-2">
          {[...Array(6)].map((_, i) => (
            <Skeleton key={i} className="h-12 rounded-lg" />
          ))}
        </div>
      ) : rows.length > 0 ? (
        <div className="overflow-hidden rounded-xl border bg-card shadow-card">
          <div className="overflow-x-auto">
            <Table>
              <TableHeader className="bg-muted/30">
                <TableRow>
                  <TableHead>{t("dependencies:list.parent")}</TableHead>
                  <TableHead className="w-12" />
                  <TableHead>{t("dependencies:list.child")}</TableHead>
                  <TableHead>{t("dependencies:list.kind")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((r) => (
                  <TableRow
                    key={r.uid}
                    data-testid="dependency-row"
                    className="transition-colors hover:bg-muted/40"
                  >
                    <TableCell>
                      {r.parent ? (
                        <Link
                          to="/orgs/$org/checks/$checkUid"
                          params={{ org, checkUid: r.parent.uid }}
                          search={{ graphPeriod: undefined, graphFull: undefined, region: undefined }}
                          className="font-medium text-foreground transition-colors hover:text-primary hover:underline"
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
                          search={{ graphPeriod: undefined, graphFull: undefined, region: undefined }}
                          className="font-medium text-foreground transition-colors hover:text-primary hover:underline"
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
          </div>
        </div>
      ) : graph && graph.edges.length > 0 ? (
        <div className="space-y-3 rounded-xl border bg-card p-12 text-center shadow-card">
          <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted">
            <Search className="h-6 w-6 text-muted-foreground" />
          </div>
          <p className="text-sm font-medium text-foreground">
            {t("dependencies:list.noMatch")}
          </p>
        </div>
      ) : (
        <div className="space-y-3 rounded-xl border bg-card p-12 text-center shadow-card">
          <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted">
            <GitBranch className="h-6 w-6 text-muted-foreground" />
          </div>
          <p className="text-sm font-medium text-foreground">
            {t("dependencies:list.empty")}
          </p>
        </div>
      )}
    </div>
  );
}
