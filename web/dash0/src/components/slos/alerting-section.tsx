import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { AlertTriangle, Flame, Pencil } from "lucide-react";
import { toast } from "sonner";

import { ApiError } from "@/api/client";
import {
  useSloAlertPolicies,
  useUpdateSloAlertPolicy,
  type SloAlertPolicy,
} from "@/api/hooks";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { StatTile } from "@/components/shared/stat-tile";
import { Switch } from "@/components/ui/switch";
import { formatWindowSeconds } from "@/lib/slo-format";

/**
 * Renders a burn rate.
 *
 * `null` means the window carried no countable probe. It renders as a dash and
 * NEVER as 0: a burn rate of zero is "everything is fine", which is exactly the
 * wrong thing to say about a window nobody was watching — the same rule the
 * attainment readout follows.
 */
function formatBurnRate(rate: number | null): string {
  if (rate === null || rate === undefined) {
    return "—";
  }
  return `${rate.toFixed(1)}x`;
}

function PolicyCard({
  policy,
  org,
  uid,
}: {
  policy: SloAlertPolicy;
  org: string;
  uid: string;
}) {
  const { t } = useTranslation("slos");
  const updatePolicy = useUpdateSloAlertPolicy(org, uid);

  const toggle = async (enabled: boolean) => {
    try {
      await updatePolicy.mutateAsync({ policyUid: policy.uid, request: { enabled } });
      toast.success(enabled ? t("alerting.enabled") : t("alerting.disabled"));
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t("alerting.toggleFailed"));
    }
  };

  const switchId = `slo-alert-policy-${policy.kind}`;
  // Inconclusive is a THIRD state, distinct from "not firing": the operator has
  // to be able to tell "the burn is fine" from "there is not enough data to
  // say", or a silently sparse check reads as a healthy one.
  const inconclusive = !policy.longConclusive || !policy.shortConclusive;

  return (
    <div
      className="space-y-4 rounded-lg border p-4"
      data-testid={`slo-alert-policy-${policy.kind}`}
    >
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0 flex-1 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium">{t(`alerting.kind.${policy.kind}`)}</span>
            <Badge variant="outline">{t(`alerting.severity.${policy.severity}`)}</Badge>
            {policy.firing && (
              <Badge variant="destructive" data-testid="slo-alert-firing">
                <Flame className="mr-1 h-3 w-3" />
                {t("alerting.firing")}
              </Badge>
            )}
          </div>
          <p className="text-sm text-muted-foreground">
            {t("alerting.rule", {
              threshold: `${policy.threshold}x`,
              long: formatWindowSeconds(policy.longWindowSeconds),
              short: formatWindowSeconds(policy.shortWindowSeconds),
            })}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <Switch
            id={switchId}
            checked={policy.enabled}
            disabled={updatePolicy.isPending}
            onCheckedChange={(checked) => void toggle(checked)}
            data-testid={`slo-alert-toggle-${policy.kind}`}
          />
          <Label htmlFor={switchId}>{t("alerting.enable")}</Label>
          <Button
            asChild
            variant="ghost"
            size="icon"
            aria-label={t("alerting.editThresholds")}
          >
            <Link
              to="/orgs/$org/slos/$uid/alert-policies/$policyUid"
              params={{ org, uid, policyUid: policy.uid }}
              data-testid={`slo-alert-edit-${policy.kind}`}
            >
              <Pencil className="h-4 w-4" />
            </Link>
          </Button>
        </div>
      </div>

      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        <StatTile
          label={t("alerting.longBurn", { window: formatWindowSeconds(policy.longWindowSeconds) })}
          value={formatBurnRate(policy.longBurnRate)}
          tone={policy.overThresholdNow ? "destructive" : "default"}
        />
        <StatTile
          label={t("alerting.shortBurn", {
            window: formatWindowSeconds(policy.shortWindowSeconds),
          })}
          value={formatBurnRate(policy.shortBurnRate)}
          tone={policy.overThresholdNow ? "destructive" : "default"}
        />
        <StatTile label={t("alerting.threshold")} value={`${policy.threshold}x`} />
        <StatTile label={t("alerting.minSamples")} value={policy.minSamples} />
      </div>

      {inconclusive && (
        <p
          className="flex items-start gap-1.5 text-xs text-muted-foreground"
          data-testid={`slo-alert-inconclusive-${policy.kind}`}
        >
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          {t("alerting.inconclusive", {
            long: policy.longSamples,
            short: policy.shortSamples,
            min: policy.minSamples,
          })}
        </p>
      )}

      {policy.firing && policy.incidentUid && (
        <Button asChild variant="outline" size="sm" className="w-full sm:w-auto">
          <Link
            to="/orgs/$org/incidents/$incidentUid"
            params={{ org, incidentUid: policy.incidentUid }}
            data-testid={`slo-alert-incident-${policy.kind}`}
          >
            {policy.incidentNumber
              ? t("alerting.viewIncidentNumbered", { number: policy.incidentNumber })
              : t("alerting.viewIncident")}
          </Link>
        </Button>
      )}

      {!policy.firing && policy.resolvingSince && (
        <p className="text-xs text-muted-foreground">{t("alerting.resolving")}</p>
      )}
    </div>
  );
}

/**
 * The SLO detail page's Alerting section.
 *
 * Toggling a policy is a single-field action and stays inline; changing the
 * thresholds and windows is a form and therefore navigates to the objective's
 * dedicated edit route, per the repo's "editing always navigates" convention.
 */
export function AlertingSection({ org, uid }: { org: string; uid: string }) {
  const { t } = useTranslation("slos");
  const { data: policies, isLoading } = useSloAlertPolicies(org, uid);

  return (
    <Card data-testid="slo-alerting-card">
      <CardHeader>
        <CardTitle className="text-base">{t("alerting.title")}</CardTitle>
        <p className="mt-1 text-sm text-muted-foreground">{t("alerting.subtitle")}</p>
      </CardHeader>
      <CardContent className="space-y-4">
        {isLoading ? (
          <Skeleton className="h-40 w-full" />
        ) : !policies || policies.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("alerting.empty")}</p>
        ) : (
          policies.map((policy) => (
            <PolicyCard key={policy.uid} policy={policy} org={org} uid={uid} />
          ))
        )}
      </CardContent>
    </Card>
  );
}
