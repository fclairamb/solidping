import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Save } from "lucide-react";

import { useMembers, type OnCallRotationType } from "@/api/hooks";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";

export interface OnCallScheduleFormValues {
  name: string;
  slug: string;
  description: string;
  timezone: string;
  rotationType: OnCallRotationType;
  handoffTime: string;
  handoffWeekday: number;
  startAt: string;
  userUids: string[];
}

interface Props {
  mode: "create" | "edit";
  org: string;
  initialValues?: Partial<OnCallScheduleFormValues>;
  onSubmit: (values: OnCallScheduleFormValues) => Promise<void> | void;
  onCancel: () => void;
  submitting?: boolean;
  error?: string | null;
}

// Default-from-browser timezone is the v1 simplification — orgs don't carry a
// timezone yet. Falls back to UTC if Intl is unavailable for any reason.
function defaultBrowserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

function nextMondayAt9amISO(): string {
  const now = new Date();
  const dayDiff = (8 - now.getDay()) % 7 || 7;
  const mon = new Date(now);
  mon.setDate(now.getDate() + dayDiff);
  mon.setHours(9, 0, 0, 0);
  return mon.toISOString();
}

function buildDefaults(): OnCallScheduleFormValues {
  return {
    name: "",
    slug: "",
    description: "",
    timezone: defaultBrowserTimezone(),
    rotationType: "weekly",
    handoffTime: "09:00",
    handoffWeekday: 0,
    startAt: nextMondayAt9amISO(),
    userUids: [],
  };
}

export function OnCallScheduleForm({
  mode,
  org,
  initialValues,
  onSubmit,
  onCancel,
  submitting,
  error,
}: Props) {
  const { t } = useTranslation(["oncall", "common"]);

  const [values, setValues] = useState<OnCallScheduleFormValues>(() => ({
    ...buildDefaults(),
    ...(initialValues ?? {}),
  }));

  // When editing, the schedule data may load after first render — re-seed
  // the form once the parent passes a hydrated `initialValues`.
  useEffect(() => {
    if (mode !== "edit" || !initialValues) return;
    setValues((prev) => ({ ...prev, ...initialValues }));
  }, [mode, initialValues]);

  const { data: membersResp } = useMembers(org);
  const members = useMemo(() => membersResp?.data ?? [], [membersResp]);

  const set = <K extends keyof OnCallScheduleFormValues>(
    key: K,
    next: OnCallScheduleFormValues[K],
  ) => setValues((prev) => ({ ...prev, [key]: next }));

  const toggleUser = (uid: string) => {
    setValues((prev) => ({
      ...prev,
      userUids: prev.userUids.includes(uid)
        ? prev.userUids.filter((u) => u !== uid)
        : [...prev.userUids, uid],
    }));
  };

  const handleSubmit = (event: React.FormEvent) => {
    event.preventDefault();
    void onSubmit({
      ...values,
      // Strip handoffWeekday for daily rotations — the backend rejects it.
      handoffWeekday:
        values.rotationType === "weekly" ? values.handoffWeekday : 0,
    });
  };

  const isEdit = mode === "edit";

  return (
    <form className="space-y-4" onSubmit={handleSubmit}>
      <div className="space-y-2">
        <Label htmlFor="oncall-name">{t("oncall:form.name")}</Label>
        <Input
          id="oncall-name"
          value={values.name}
          onChange={(e) => set("name", e.target.value)}
          placeholder={t("oncall:form.namePlaceholder")}
          required
        />
      </div>

      <div className="space-y-2">
        <Label htmlFor="oncall-slug">{t("oncall:form.slug")}</Label>
        <Input
          id="oncall-slug"
          value={values.slug}
          onChange={(e) => set("slug", e.target.value)}
          placeholder={t("oncall:form.slugPlaceholder")}
          required
          pattern="[a-z0-9-]+"
          disabled={isEdit}
        />
        {isEdit && (
          <p className="text-xs text-muted-foreground">
            {t("oncall:form.slugReadOnly")}
          </p>
        )}
      </div>

      <div className="space-y-2">
        <Label htmlFor="oncall-desc">{t("oncall:form.description")}</Label>
        <Textarea
          id="oncall-desc"
          value={values.description}
          onChange={(e) => set("description", e.target.value)}
          placeholder={t("oncall:form.descriptionPlaceholder")}
        />
      </div>

      <div className="space-y-2">
        <Label htmlFor="oncall-tz">{t("oncall:form.timezone")}</Label>
        <Input
          id="oncall-tz"
          value={values.timezone}
          onChange={(e) => set("timezone", e.target.value)}
          placeholder={t("oncall:form.timezonePlaceholder")}
          required
        />
      </div>

      <div className="grid grid-cols-2 gap-4">
        <div className="space-y-2">
          <Label>{t("oncall:form.rotationType")}</Label>
          <Select
            value={values.rotationType}
            onValueChange={(v) => set("rotationType", v as OnCallRotationType)}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="daily">{t("oncall:detail.rotationDaily")}</SelectItem>
              <SelectItem value="weekly">{t("oncall:detail.rotationWeekly")}</SelectItem>
            </SelectContent>
          </Select>
        </div>

        <div className="space-y-2">
          <Label htmlFor="oncall-hot">{t("oncall:form.handoffTime")}</Label>
          <Input
            id="oncall-hot"
            value={values.handoffTime}
            onChange={(e) => set("handoffTime", e.target.value)}
            placeholder={t("oncall:form.handoffTimePlaceholder")}
            pattern="[0-2][0-9]:[0-5][0-9]"
            required
          />
        </div>
      </div>

      {values.rotationType === "weekly" && (
        <div className="space-y-2">
          <Label>{t("oncall:form.handoffWeekday")}</Label>
          <Select
            value={String(values.handoffWeekday)}
            onValueChange={(v) => set("handoffWeekday", Number(v))}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"] as const).map(
                (day, idx) => (
                  <SelectItem key={day} value={String(idx)}>
                    {t(`oncall:form.weekday${day}`)}
                  </SelectItem>
                ),
              )}
            </SelectContent>
          </Select>
        </div>
      )}

      <div className="space-y-2">
        <Label htmlFor="oncall-start">{t("oncall:form.startAt")}</Label>
        <Input
          id="oncall-start"
          value={values.startAt}
          onChange={(e) => set("startAt", e.target.value)}
          required
        />
        <p className="text-xs text-muted-foreground">
          {t("oncall:form.startAtHelp")}
        </p>
        {isEdit && (
          <p className="text-xs text-amber-600 dark:text-amber-400">
            {t("oncall:form.startAtWarning")}
          </p>
        )}
      </div>

      <div className="space-y-2">
        <Label>{t("oncall:form.userUids")}</Label>
        <p className="text-xs text-muted-foreground">
          {t("oncall:form.userUidsHelp")}
        </p>
        <div className="border rounded p-2 max-h-64 overflow-y-auto">
          {members.length === 0 ? (
            <p className="text-sm text-muted-foreground py-2">
              {t("oncall:detail.noUsers")}
            </p>
          ) : (
            members.map((m) => (
              <label
                key={m.userUid}
                className="flex items-center gap-2 py-1 cursor-pointer"
              >
                <input
                  type="checkbox"
                  checked={values.userUids.includes(m.userUid)}
                  onChange={() => toggleUser(m.userUid)}
                />
                <span className="text-sm">
                  {m.name || m.email}{" "}
                  <span className="text-muted-foreground">({m.email})</span>
                </span>
              </label>
            ))
          )}
        </div>
      </div>

      {error && (
        <p className="text-sm text-destructive" role="alert">
          {error}
        </p>
      )}

      <div className="flex justify-end gap-2">
        <Button type="button" variant="outline" onClick={onCancel}>
          {t("oncall:form.cancel")}
        </Button>
        <Button
          type="submit"
          disabled={submitting}
          aria-label={t("oncall:form.submit")}
        >
          <Save />
          <span className="hidden sm:inline">{t("oncall:form.submit")}</span>
        </Button>
      </div>
    </form>
  );
}
