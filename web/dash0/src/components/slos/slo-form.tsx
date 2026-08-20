import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Target } from "lucide-react";

import type { CreateSloRequest, Slo } from "@/api/hooks";
import { CheckPicker } from "@/components/shared/check-picker";
import { CheckGroupPicker } from "@/components/shared/check-group-picker";
import { PageHeader } from "@/components/shared/page-header";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { SegmentedControl } from "@/components/ui/segmented-control";
import { Switch } from "@/components/ui/switch";

export interface SloFormProps {
  org: string;
  mode: "create" | "edit";
  initial?: Slo;
  isPending?: boolean;
  onSubmit: (request: CreateSloRequest) => void;
  onCancel: () => void;
}

const DEFAULT_TARGET = 99.9;

/**
 * The create/edit form for a service-level objective.
 *
 * The initial values seed React state once. Callers render this form only after
 * the objective has loaded and key it by UID, so there is no later "sync props
 * into state" effect to write (and no cascading render to pay for).
 *
 * Scope is a segmented control rather than two independent pickers on purpose:
 * the schema enforces an XOR, and a UI that lets you fill both only to reject
 * the save is a worse teacher than one that cannot express the invalid state.
 */
export function SloForm({ org, mode, initial, isPending, onSubmit, onCancel }: SloFormProps) {
  const { t } = useTranslation(["slos", "common"]);

  const [name, setName] = useState(initial?.name ?? "");
  const [slug, setSlug] = useState(initial?.slug ?? "");
  const [scopeKind, setScopeKind] = useState<"check" | "group">(
    initial?.checkGroupUid ? "group" : "check",
  );
  const [checkUid, setCheckUid] = useState<string | undefined>(initial?.checkUid);
  const [checkGroupUid, setCheckGroupUid] = useState<string | undefined>(initial?.checkGroupUid);
  const [targetPct, setTargetPct] = useState(String(initial?.targetPct ?? DEFAULT_TARGET));
  const [timezone, setTimezone] = useState(
    initial?.timezone ?? Intl.DateTimeFormat().resolvedOptions().timeZone ?? "UTC",
  );
  const [excludeMaintenance, setExcludeMaintenance] = useState(
    initial?.excludeMaintenance ?? true,
  );
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);

  const parsedTarget = Number(targetPct);
  const targetValid = Number.isFinite(parsedTarget) && parsedTarget > 0 && parsedTarget <= 100;
  const scopeValid = scopeKind === "check" ? !!checkUid : !!checkGroupUid;
  const canSubmit = name.trim().length > 0 && targetValid && scopeValid && !isPending;

  return (
    <form
      className="space-y-6"
      data-testid="slo-form"
      onSubmit={(event) => {
        event.preventDefault();
        if (!canSubmit) return;
        onSubmit({
          name: name.trim(),
          slug: slug.trim() || undefined,
          // Both sides are always sent so a scope change clears the other
          // column in the same write — the XOR constraint rejects anything else.
          checkUid: scopeKind === "check" ? checkUid : null,
          checkGroupUid: scopeKind === "group" ? checkGroupUid : null,
          targetPct: parsedTarget,
          timezone,
          excludeMaintenance,
          enabled,
        });
      }}
    >
      <PageHeader
        icon={Target}
        title={mode === "create" ? t("slos:form.newTitle") : t("slos:form.editTitle")}
        description={t("slos:layout.subtitle")}
      />

      <Card>
        <CardContent className="space-y-5 pt-6">
          <div className="space-y-2">
            <Label htmlFor="slo-name">{t("slos:form.name")}</Label>
            <Input
              id="slo-name"
              data-testid="slo-name"
              value={name}
              placeholder={t("slos:form.namePlaceholder")}
              onChange={(event) => setName(event.target.value)}
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="slo-slug">{t("slos:form.slug")}</Label>
            <Input
              id="slo-slug"
              data-testid="slo-slug"
              value={slug}
              onChange={(event) => setSlug(event.target.value)}
            />
            <p className="text-xs text-muted-foreground">{t("slos:form.slugHelp")}</p>
          </div>

          <div className="space-y-2">
            <Label>{t("slos:form.scope")}</Label>
            <SegmentedControl
              value={scopeKind}
              onValueChange={(value) => setScopeKind(value as "check" | "group")}
              options={[
                { value: "check", label: t("slos:form.scopeCheck"), testId: "slo-scope-check" },
                { value: "group", label: t("slos:form.scopeGroup"), testId: "slo-scope-group" },
              ]}
            />
            <div className="pt-2">
              {scopeKind === "check" ? (
                <CheckPicker
                  org={org}
                  value={checkUid}
                  onChange={(uid) => setCheckUid(uid)}
                  placeholder={t("slos:form.selectCheck")}
                  triggerTestId="slo-check-select"
                />
              ) : (
                <CheckGroupPicker
                  org={org}
                  value={checkGroupUid}
                  onChange={(uid) => setCheckGroupUid(uid)}
                  placeholder={t("slos:form.selectGroup")}
                  triggerTestId="slo-group-select"
                />
              )}
            </div>
            <p className="text-xs text-muted-foreground">{t("slos:form.scopeHelp")}</p>
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="slo-target">{t("slos:form.target")}</Label>
              <Input
                id="slo-target"
                data-testid="slo-target"
                inputMode="decimal"
                value={targetPct}
                onChange={(event) => setTargetPct(event.target.value)}
              />
              <p className="text-xs text-muted-foreground">{t("slos:form.targetHelp")}</p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="slo-timezone">{t("slos:form.timezone")}</Label>
              <Input
                id="slo-timezone"
                data-testid="slo-timezone"
                value={timezone}
                onChange={(event) => setTimezone(event.target.value)}
              />
              <p className="text-xs text-muted-foreground">{t("slos:form.timezoneHelp")}</p>
            </div>
          </div>

          <div className="flex items-start justify-between gap-4 rounded-md border p-3">
            <div className="min-w-0">
              <Label htmlFor="slo-exclude-maintenance">
                {t("slos:form.excludeMaintenance")}
              </Label>
              <p className="mt-1 text-xs text-muted-foreground">
                {t("slos:form.excludeMaintenanceHelp")}
              </p>
            </div>
            <Switch
              id="slo-exclude-maintenance"
              data-testid="slo-exclude-maintenance"
              checked={excludeMaintenance}
              onCheckedChange={setExcludeMaintenance}
            />
          </div>

          <div className="flex items-center justify-between gap-4">
            <Label htmlFor="slo-enabled">{t("slos:form.enabled")}</Label>
            <Switch
              id="slo-enabled"
              data-testid="slo-enabled"
              checked={enabled}
              onCheckedChange={setEnabled}
            />
          </div>
        </CardContent>
      </Card>

      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={!canSubmit} data-testid="slo-submit">
          {mode === "create" ? t("slos:form.create") : t("slos:form.save")}
        </Button>
        <Button type="button" variant="outline" onClick={onCancel}>
          {t("slos:form.cancel")}
        </Button>
      </div>
    </form>
  );
}
