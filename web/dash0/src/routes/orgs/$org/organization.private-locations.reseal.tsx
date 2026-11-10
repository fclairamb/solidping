import { useTranslation } from "react-i18next";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ArrowLeft, KeyRound } from "lucide-react";

import { useResealChecks } from "@/api/hooks";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorView } from "@/components/shared/error-views";

// The Re-seal page (spec 2026-11-08-01 §7): the checks whose sealed secrets
// no longer match their regions' agents (or region key). The server cannot
// re-wrap them, so each opens its form in Replace mode to type them again.
export const Route = createFileRoute("/orgs/$org/organization/private-locations/reseal")({
  component: ResealPage,
});

function ResealPage() {
  const { t } = useTranslation(["org"]);
  const { org } = Route.useParams();
  const { data: checks, isLoading, error, refetch } = useResealChecks(org);

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource="Checks"
        backTo="/orgs/$org/organization/private-locations"
        backLabel={t("privateLocations.reseal.back")}
        onRetry={() => refetch()}
      />
    );
  }

  return (
    <div className="max-w-3xl space-y-4">
      <Button asChild variant="ghost" size="sm" data-testid="reseal-back-link">
        <Link to="/orgs/$org/organization/private-locations" params={{ org }} data-testid="reseal-back-link">
          <ArrowLeft className="h-4 w-4" />
          {t("privateLocations.reseal.back")}
        </Link>
      </Button>
      <Card data-testid="reseal-card">
        <CardHeader>
          <CardTitle>{t("privateLocations.reseal.title")}</CardTitle>
          <CardDescription>{t("privateLocations.reseal.description")}</CardDescription>
        </CardHeader>
        <CardContent>
          {isLoading ? (
            <Skeleton className="h-16 w-full" />
          ) : (checks?.length ?? 0) === 0 ? (
            <p className="text-sm text-muted-foreground" data-testid="reseal-empty">
              {t("privateLocations.reseal.empty")}
            </p>
          ) : (
            <ul className="divide-y">
              {checks?.map((check) => (
                <li
                  key={check.uid}
                  className="flex flex-wrap items-center justify-between gap-2 py-2"
                  data-testid="reseal-check-item"
                >
                  <div className="min-w-0">
                    <p className="truncate text-sm font-medium">{check.name}</p>
                    <div className="flex flex-wrap gap-1">
                      {check.regions.map((region) => (
                        <Badge key={region} variant="secondary" className="text-[10px]">
                          {region}
                        </Badge>
                      ))}
                    </div>
                  </div>
                  <Button asChild size="sm" variant="outline" data-testid={`reseal-check-link-${check.uid}`}>
                    <Link
                      to="/orgs/$org/checks/$checkUid/edit"
                      params={{ org, checkUid: check.uid }}
                      search={{ replace: true }}
                      data-testid={`reseal-check-link-${check.uid}`}
                    >
                      <KeyRound className="h-4 w-4" />
                      {t("privateLocations.reseal.open")}
                    </Link>
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
