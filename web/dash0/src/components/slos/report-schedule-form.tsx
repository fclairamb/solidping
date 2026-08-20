import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Mail } from "lucide-react";

import type { CreateReportScheduleRequest, ReportFrequency, ReportSchedule } from "@/api/hooks";
import { CheckMultiPicker } from "@/components/shared/check-multi-picker";
import { PageHeader } from "@/components/shared/page-header";
import { RecipientsInput } from "@/components/shared/recipients-input";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { SegmentedControl } from "@/components/ui/segmented-control";
import { Switch } from "@/components/ui/switch";

export interface ReportScheduleFormProps {
  org: string;
  mode: "create" | "edit";
  initial?: ReportSchedule;
  isPending?: boolean;
  onSubmit: (request: CreateReportScheduleRequest) => void;
  onCancel: () => void;
}

/**
 * Initial values seed React state once. Callers render this form only after the
 * schedule has loaded and key it by UID, so no props-into-state effect is
 * needed.
 */
export function ReportScheduleForm({
  org,
  mode,
  initial,
  isPending,
  onSubmit,
  onCancel,
}: ReportScheduleFormProps) {
  const { t } = useTranslation("slos");

  const [name, setName] = useState(initial?.name ?? "");
  const [frequency, setFrequency] = useState<ReportFrequency>(initial?.frequency ?? "monthly");
  const [timezone, setTimezone] = useState(
    initial?.timezone ?? Intl.DateTimeFormat().resolvedOptions().timeZone ?? "UTC",
  );
  const [recipients, setRecipients] = useState<string[]>(initial?.recipients ?? []);
  const [checkUids, setCheckUids] = useState<string[]>(initial?.checkUids ?? []);
  const [checkGroupUids, setCheckGroupUids] = useState<string[]>(initial?.checkGroupUids ?? []);
  const [includeSlos, setIncludeSlos] = useState(initial?.includeSlos ?? true);
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);

  const canSubmit = name.trim().length > 0 && recipients.length > 0 && !isPending;

  return (
    <form
      className="space-y-6"
      data-testid="report-schedule-form"
      onSubmit={(event) => {
        event.preventDefault();
        if (!canSubmit) return;
        onSubmit({
          name: name.trim(),
          frequency,
          timezone,
          recipients,
          checkUids,
          checkGroupUids,
          includeSlos,
          enabled,
        });
      }}
    >
      <PageHeader
        icon={Mail}
        title={mode === "create" ? t("reports.form.newTitle") : t("reports.form.editTitle")}
        description={t("reports.subtitle")}
      />

      <Card>
        <CardContent className="space-y-5 pt-6">
          <div className="space-y-2">
            <Label htmlFor="report-name">{t("reports.form.name")}</Label>
            <Input
              id="report-name"
              data-testid="report-name"
              value={name}
              placeholder={t("reports.form.namePlaceholder")}
              onChange={(event) => setName(event.target.value)}
            />
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label>{t("reports.form.frequency")}</Label>
              <SegmentedControl
                value={frequency}
                onValueChange={(value) => setFrequency(value as ReportFrequency)}
                options={[
                  {
                    value: "weekly",
                    label: t("reports.frequency.weekly"),
                    testId: "report-frequency-weekly",
                  },
                  {
                    value: "monthly",
                    label: t("reports.frequency.monthly"),
                    testId: "report-frequency-monthly",
                  },
                ]}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="report-timezone">{t("reports.form.timezone")}</Label>
              <Input
                id="report-timezone"
                data-testid="report-timezone"
                value={timezone}
                onChange={(event) => setTimezone(event.target.value)}
              />
              <p className="text-xs text-muted-foreground">{t("reports.form.timezoneHelp")}</p>
            </div>
          </div>

          <div className="space-y-2">
            <Label htmlFor="report-recipients">{t("reports.form.recipients")}</Label>
            <RecipientsInput
              id="report-recipients"
              value={recipients}
              onChange={setRecipients}
              data-testid="report-recipients"
            />
            <p className="text-xs text-muted-foreground">{t("reports.form.recipientsHelp")}</p>
          </div>

          <div className="space-y-2">
            <Label>{t("reports.form.checks")}</Label>
            <CheckMultiPicker
              org={org}
              kind="checks"
              value={checkUids}
              onChange={setCheckUids}
              data-testid="report-checks"
            />
          </div>

          <div className="space-y-2">
            <Label>{t("reports.form.groups")}</Label>
            <CheckMultiPicker
              org={org}
              kind="groups"
              value={checkGroupUids}
              onChange={setCheckGroupUids}
              data-testid="report-groups"
            />
            <p className="text-xs text-muted-foreground">{t("reports.form.scopeHelp")}</p>
          </div>

          <div className="flex items-start justify-between gap-4 rounded-md border p-3">
            <div className="min-w-0">
              <Label htmlFor="report-include-slos">{t("reports.form.includeSlos")}</Label>
              <p className="mt-1 text-xs text-muted-foreground">
                {t("reports.form.includeSlosHelp")}
              </p>
            </div>
            <Switch
              id="report-include-slos"
              data-testid="report-include-slos"
              checked={includeSlos}
              onCheckedChange={setIncludeSlos}
            />
          </div>

          <div className="flex items-center justify-between gap-4">
            <Label htmlFor="report-enabled">{t("reports.form.enabled")}</Label>
            <Switch
              id="report-enabled"
              data-testid="report-enabled"
              checked={enabled}
              onCheckedChange={setEnabled}
            />
          </div>
        </CardContent>
      </Card>

      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={!canSubmit} data-testid="report-submit">
          {mode === "create" ? t("reports.form.create") : t("reports.form.save")}
        </Button>
        <Button type="button" variant="outline" onClick={onCancel}>
          {t("reports.form.cancel")}
        </Button>
      </div>
    </form>
  );
}
