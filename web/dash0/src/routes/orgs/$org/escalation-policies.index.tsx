import { createFileRoute, Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { ArrowUpRight, Plus, Repeat } from "lucide-react";

import { useEscalationPolicies } from "@/api/hooks";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorView } from "@/components/shared/error-views";

export const Route = createFileRoute("/orgs/$org/escalation-policies/")({
  component: EscalationPoliciesListPage,
});

function EscalationPoliciesListPage() {
  const { t } = useTranslation(["escalation", "common"]);
  const { org } = Route.useParams();
  const { data: policies, isLoading, error, refetch } =
    useEscalationPolicies(org);

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

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-3xl font-bold tracking-tight flex items-center gap-2">
            <ArrowUpRight className="h-7 w-7 text-muted-foreground" />
            {t("escalation:list.title")}
          </h1>
          <p className="text-muted-foreground">
            {t("escalation:list.subtitle")}
          </p>
        </div>
        <Button asChild>
          <Link to="/orgs/$org/escalation-policies/new" params={{ org }}>
            <Plus className="h-4 w-4 mr-1" />
            {t("escalation:list.create")}
          </Link>
        </Button>
      </div>

      {isLoading ? (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {[...Array(3)].map((_, i) => (
            <Skeleton key={i} className="h-40 rounded" />
          ))}
        </div>
      ) : !policies || policies.length === 0 ? (
        <Card>
          <CardContent className="py-10 text-center text-sm text-muted-foreground">
            {t("escalation:list.empty")}
          </CardContent>
        </Card>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {policies.map((policy) => (
            <Card
              key={policy.uid}
              className="hover:shadow-md transition-shadow"
            >
              <CardHeader>
                <CardTitle>
                  <Link
                    to="/orgs/$org/escalation-policies/$slug"
                    params={{ org, slug: policy.slug }}
                    className="hover:underline"
                  >
                    {policy.name}
                  </Link>
                </CardTitle>
                {policy.description && (
                  <CardDescription>{policy.description}</CardDescription>
                )}
              </CardHeader>
              <CardContent className="space-y-2 text-sm text-muted-foreground">
                {policy.repeatMax > 0 && policy.repeatAfterMinutes && (
                  <div className="flex items-center gap-1">
                    <Repeat className="h-3.5 w-3.5" />
                    {t("escalation:list.repeats", {
                      count: policy.repeatMax,
                      minutes: policy.repeatAfterMinutes,
                    })}
                  </div>
                )}
                <div>{t("escalation:list.editToSeeSteps")}</div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}
    </div>
  );
}
