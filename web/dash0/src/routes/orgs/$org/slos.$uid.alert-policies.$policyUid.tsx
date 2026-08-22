import { useState } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Flame } from "lucide-react";
import { toast } from "sonner";

import { ApiError } from "@/api/client";
import {
  useSloAlertPolicies,
  useUpdateSloAlertPolicy,
  type SloAlertPolicy,
  type SloAlertSeverity,
} from "@/api/hooks";
import { PageHeader } from "@/components/shared/page-header";
import { QueryErrorView } from "@/components/shared/error-views";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { SegmentedControl } from "@/components/ui/segmented-control";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";

export const Route = createFileRoute("/orgs/$org/slos/$uid/alert-policies/$policyUid")({
  component: SloAlertPolicyEditPage,
});

/** Seven days — matches the server-side cap on the long window. */
const MAX_WINDOW_MINUTES = 7 * 24 * 60;

/**
 * Windows are stored in seconds but nobody thinks in seconds about a "1h" or a
 * "5m" alerting window, so the form works in minutes and converts at the edge.
 */
function PolicyForm({
  policy,
  org,
  uid,
}: {
  policy: SloAlertPolicy;
  org: string;
  uid: string;
}) {
  const { t } = useTranslation(["slos", "common"]);
  const navigate = useNavigate();
  const updatePolicy = useUpdateSloAlertPolicy(org, uid);

  const [enabled, setEnabled] = useState(policy.enabled);
  const [longMinutes, setLongMinutes] = useState(String(policy.longWindowSeconds / 60));
  const [shortMinutes, setShortMinutes] = useState(String(policy.shortWindowSeconds / 60));
  const [threshold, setThreshold] = useState(String(policy.threshold));
  const [severity, setSeverity] = useState<SloAlertSeverity>(policy.severity);
  const [minSamples, setMinSamples] = useState(String(policy.minSamples));

  const parsedLong = Number(longMinutes);
  const parsedShort = Number(shortMinutes);
  const parsedThreshold = Number(threshold);
  const parsedMinSamples = Number(minSamples);

  const windowsValid =
    Number.isFinite(parsedLong) &&
    Number.isFinite(parsedShort) &&
    parsedShort > 0 &&
    parsedLong > 0 &&
    parsedShort <= parsedLong &&
    parsedLong <= MAX_WINDOW_MINUTES;
  const thresholdValid = Number.isFinite(parsedThreshold) && parsedThreshold > 0;
  const minSamplesValid = Number.isInteger(parsedMinSamples) && parsedMinSamples >= 1;
  const canSubmit = windowsValid && thresholdValid && minSamplesValid && !updatePolicy.isPending;

  const back = () => navigate({ to: "/orgs/$org/slos/$uid", params: { org, uid } });

  return (
    <form
      className="space-y-6"
      data-testid="slo-alert-policy-form"
      onSubmit={async (event) => {
        event.preventDefault();
        if (!canSubmit) return;

        try {
          await updatePolicy.mutateAsync({
            policyUid: policy.uid,
            request: {
              enabled,
              longWindowSeconds: Math.round(parsedLong * 60),
              shortWindowSeconds: Math.round(parsedShort * 60),
              threshold: parsedThreshold,
              severity,
              minSamples: parsedMinSamples,
            },
          });
          toast.success(t("slos:alerting.saved"));
          back();
        } catch (err) {
          toast.error(err instanceof ApiError ? err.message : t("slos:alerting.saveFailed"));
        }
      }}
    >
      <PageHeader
        icon={Flame}
        title={t(`slos:alerting.kind.${policy.kind}`)}
        description={t("slos:alerting.formSubtitle")}
      />

      <Card>
        <CardContent className="space-y-5 pt-6">
          <div className="flex items-start justify-between gap-4 rounded-md border p-3">
            <div className="min-w-0">
              <Label htmlFor="policy-enabled">{t("slos:alerting.enable")}</Label>
              <p className="mt-1 text-xs text-muted-foreground">
                {t("slos:alerting.enableHelp")}
              </p>
            </div>
            <Switch
              id="policy-enabled"
              data-testid="policy-enabled"
              checked={enabled}
              onCheckedChange={setEnabled}
            />
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="policy-long">{t("slos:alerting.longWindow")}</Label>
              <Input
                id="policy-long"
                data-testid="policy-long-window"
                inputMode="decimal"
                value={longMinutes}
                onChange={(event) => setLongMinutes(event.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                {t("slos:alerting.longWindowHelp")}
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="policy-short">{t("slos:alerting.shortWindow")}</Label>
              <Input
                id="policy-short"
                data-testid="policy-short-window"
                inputMode="decimal"
                value={shortMinutes}
                onChange={(event) => setShortMinutes(event.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                {t("slos:alerting.shortWindowHelp")}
              </p>
            </div>
          </div>

          {!windowsValid && (
            <p className="text-xs text-destructive" data-testid="policy-windows-error">
              {t("slos:alerting.windowsInvalid")}
            </p>
          )}

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="policy-threshold">{t("slos:alerting.threshold")}</Label>
              <Input
                id="policy-threshold"
                data-testid="policy-threshold"
                inputMode="decimal"
                value={threshold}
                onChange={(event) => setThreshold(event.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                {t("slos:alerting.thresholdHelp")}
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="policy-min-samples">{t("slos:alerting.minSamples")}</Label>
              <Input
                id="policy-min-samples"
                data-testid="policy-min-samples"
                inputMode="numeric"
                value={minSamples}
                onChange={(event) => setMinSamples(event.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                {t("slos:alerting.minSamplesHelp")}
              </p>
            </div>
          </div>

          <div className="space-y-2">
            <Label>{t("slos:alerting.severityLabel")}</Label>
            <SegmentedControl
              value={severity}
              onValueChange={(value) => setSeverity(value as SloAlertSeverity)}
              options={[
                {
                  value: "warning",
                  label: t("slos:alerting.severity.warning"),
                  testId: "policy-severity-warning",
                },
                {
                  value: "critical",
                  label: t("slos:alerting.severity.critical"),
                  testId: "policy-severity-critical",
                },
              ]}
            />
            <p className="text-xs text-muted-foreground">{t("slos:alerting.severityHelp")}</p>
          </div>
        </CardContent>
      </Card>

      <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
        <Button type="button" variant="outline" onClick={back}>
          {t("slos:form.cancel")}
        </Button>
        <Button type="submit" disabled={!canSubmit} data-testid="policy-save">
          {t("slos:form.save")}
        </Button>
      </div>
    </form>
  );
}

function SloAlertPolicyEditPage() {
  const { org, uid, policyUid } = Route.useParams();
  const { data: policies, isLoading, error, refetch } = useSloAlertPolicies(org, uid);

  if (error) {
    return <QueryErrorView error={error} org={org} onRetry={() => refetch()} />;
  }

  if (isLoading || !policies) {
    return (
      <div className="max-w-2xl space-y-6">
        <Skeleton className="h-10 w-64" />
        <Skeleton className="h-96 rounded-lg" />
      </div>
    );
  }

  const policy = policies.find((candidate) => candidate.uid === policyUid);

  if (!policy) {
    return (
      <QueryErrorView
        error={new ApiError("Alert policy not found", "NOT_FOUND", undefined, 404)}
        org={org}
      />
    );
  }

  return (
    <div className="max-w-2xl">
      <PolicyForm key={policy.uid} policy={policy} org={org} uid={uid} />
    </div>
  );
}
